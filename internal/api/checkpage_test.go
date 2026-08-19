package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The checker must actually verify, and must actually refuse.
//
// Run against a real proof this node produced, with the page's own JS executed
// under node — the same harness convention the other JS tests here use. A page
// that renders a green tick without folding the path would pass any test that
// only looked at the markup.
func TestCheckerVerifiesARealProofAndRejectsATamperedOne(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	srv, s, vkey := testServer(t)
	seedBasic(t, s)

	// A real record with a real inclusion proof.
	resp, err := http.Get(srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com?proof=inclusion")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup: %d — %s", resp.StatusCode, body)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env["proof"] == nil {
		t.Fatalf("no proof in the lookup response: %s", body)
	}

	page, err := os.ReadFile(filepath.Join("static", "check.html"))
	if err != nil {
		t.Fatal(err)
	}
	vjs, err := os.ReadFile(filepath.Join("static", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(page)
	pageJS := full[strings.LastIndex(full, "<script>")+len("<script>"):]
	pageJS = pageJS[:strings.Index(pageJS, "</script>")]
	// The two handler bindings need elements that exist; the harness supplies them.
	good, _ := json.Marshal(env)

	// Tamper: flip the leaf's digest. The record still parses, and the path is
	// untouched — only the leaf hash changes, so the fold must stop matching.
	var bad map[string]any
	json.Unmarshal(body, &bad)
	leaf := bad["proof"].(map[string]any)["leaf"].(map[string]any)
	dig, _ := leaf["digest"].(string)
	leaf["digest"] = strings.Repeat("0", len(dig))
	badJSON, _ := json.Marshal(bad)

	// The fixtures go in files, not into the script. Embedding marshalled JSON
	// in a template literal turns its \n escapes into real newlines and the
	// document stops being JSON — which is how the first run of this test
	// "failed to verify" a proof that was perfectly good.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.json"), good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), badJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := `
import fs from 'node:fs';
const els = new Map();
const fake = id => ({ id, innerHTML: '', value: '', textContent: '', onclick: null,
  addEventListener(){}, });
globalThis.document = { getElementById: id => { if(!els.has(id)) els.set(id, fake(id)); return els.get(id); } };
globalThis.location = { origin: 'http://node.example' };
globalThis.crypto = (await import('node:crypto')).webcrypto;
globalThis.fetch = async () => ({ ok: true, text: async () => '' });
const GOOD = fs.readFileSync(process.argv[2], 'utf8');
const BAD  = fs.readFileSync(process.argv[3], 'utf8');
const VKEY = process.argv[4];
`
	check := `
const out = document.getElementById('out');

document.getElementById('doc').value = GOOD;
document.getElementById('vkey').value = VKEY;
await check();
const okHTML = out.innerHTML;
if (!okHTML.includes('is in the log')) throw new Error('a real proof did not verify: ' + okHTML.slice(0, 400));
if (!okHTML.includes('signed by that key')) throw new Error('the signature was not checked: ' + okHTML.slice(0, 400));

document.getElementById('doc').value = BAD;
await check();
const badHTML = out.innerHTML;
if (badHTML.includes('\u2713 This record is in the log')) {
  throw new Error('a TAMPERED record verified — the fold is not being computed');
}
if (!badHTML.includes('does NOT fold')) throw new Error('the tamper was not reported: ' + badHTML.slice(0, 400));
console.log('OK');
`
	file := filepath.Join(dir, "check.mjs")
	src := harness + string(vjs) + "\n" + pageJS + "\n" + check
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", file,
		filepath.Join(dir, "good.json"), filepath.Join(dir, "bad.json"), vkey).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("checker: %v\n%s", err, out)
	}
}
