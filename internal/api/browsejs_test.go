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

// The browser must show the proof verdict without being asked, and must say so
// loudly when the proof does not check out.
//
// Running the shipped page's own JS is the only assertion that means anything
// here. A test that looked at the markup would happily pass on a page that
// renders the panel frame and never folds a path — which is precisely the
// failure the panel exists to make impossible.
func TestBrowseChecksTheProofOnArrivalAndRefusesABadOne(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	srv, s, vkey := testServer(t)
	seedBasic(t, s)

	get := func(path string) []byte {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d — %s", path, resp.StatusCode, b)
		}
		return b
	}

	good := get("/dedi/lookup/flywheel/participants/bap.example.com?proof=inclusion")
	versions := get("/dedi/versions/flywheel/participants/bap.example.com")
	ckpt := get("/dedi/log/checkpoint")

	// Tampered: flip the leaf digest. Everything else — the path, the
	// checkpoint, the signature — stays genuine, so only the fold can catch it.
	var bad map[string]any
	if err := json.Unmarshal(good, &bad); err != nil {
		t.Fatal(err)
	}
	leaf := bad["proof"].(map[string]any)["leaf"].(map[string]any)
	leaf["digest"] = strings.Repeat("0", len(leaf["digest"].(string)))
	badJSON, _ := json.Marshal(bad)

	// A response carrying no proof at all. Distinct from a failure: nothing was
	// checked, rather than something was checked and did not hold.
	var noproof map[string]any
	json.Unmarshal(good, &noproof)
	delete(noproof, "proof")
	noProofJSON, _ := json.Marshal(noproof)

	page, err := os.ReadFile(filepath.Join("static", "index.html"))
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

	// Fixtures travel as files. Marshalled JSON pasted into a template literal
	// has its \n escapes turned into real newlines and stops being JSON, which
	// once made this harness "fail to verify" a proof that was perfectly good.
	dir := t.TempDir()
	for name, b := range map[string][]byte{
		"good.json": good, "bad.json": badJSON, "noproof.json": noProofJSON,
		"versions.json": versions, "ckpt.txt": ckpt,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	harness := `
import fs from 'node:fs';
if (!globalThis.crypto) {
  Object.defineProperty(globalThis, 'crypto', {
    value: (await import('node:crypto')).webcrypto, configurable: true,
  });
}
const GOOD = fs.readFileSync(process.argv[2], 'utf8');
const BAD  = fs.readFileSync(process.argv[3], 'utf8');
const NOPROOF = fs.readFileSync(process.argv[4], 'utf8');
const VERSIONS = fs.readFileSync(process.argv[5], 'utf8');
const CKPT = fs.readFileSync(process.argv[6], 'utf8');
const KEY = process.argv[7];

const els = new Map();
const mk = id => ({ id, innerHTML: '', value: '', focus(){}, setSelectionRange(){},
                    scrollIntoView(){}, addEventListener(){} });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, mk(id)); return els.get(id); },
  querySelector: sel => {
    if (sel.startsWith('meta')) return { content: KEY };
    if (sel === '.rows') return mk('rows');
    return null;
  },
};
globalThis.location = { origin: 'http://node.example', pathname: '/browse', search: '' };
globalThis.history = { pushState(){} };
globalThis.addEventListener = () => {};
globalThis.matchMedia = () => ({ matches: true });
globalThis.requestAnimationFrame = fn => fn();

// The fetch stub answers what the record view actually asks for, and is
// swapped per case below.
let LOOKUP = GOOD;
globalThis.fetch = async (url) => {
  if (url.includes('/dedi/log/checkpoint')) return { ok: true, text: async () => CKPT };
  if (url.includes('/dedi/versions/'))      return { ok: true, json: async () => JSON.parse(VERSIONS) };
  if (url.includes('/dedi/lookup/'))        return { ok: true, json: async () => JSON.parse(LOOKUP) };
  if (url.includes('/dedi/query/'))         return { ok: true, json: async () => ({data:{registries:[],records:[]}}) };
  return { ok: false, status: 404, json: async () => ({}) };
};
const settle = () => new Promise(r => setTimeout(r, 50));
const P = { ns: 'flywheel', reg: 'participants', rec: 'bap.example.com', v: '' };
`

	checks := `
// 1. The verdict is present WITHOUT anything being clicked. loadRecord is the
//    only thing called; nothing touches a button.
LOOKUP = GOOD;
await loadRecord(P); await settle();
const arrival = document.getElementById('proofpanel').innerHTML;
if (!arrival.includes('checked in your browser'))
  throw new Error('a valid proof was not verified on arrival: ' + arrival.slice(0, 500));
if (arrival.includes('FAILED'))
  throw new Error('a valid proof reported failure: ' + arrival.slice(0, 500));

// 2. A tampered leaf must produce a LOUD failure, not a quiet one and not a
//    blank panel. This is the page's most important possible output.
LOOKUP = BAD;
await loadRecord(P); await settle();
const tampered = document.getElementById('proofpanel').innerHTML;
if (!tampered.includes('FAILED'))
  throw new Error('a tampered proof did not report failure: ' + tampered.slice(0, 500));
if (!tampered.includes('does NOT match'))
  throw new Error('the failure did not say what failed: ' + tampered.slice(0, 500));

// 3. No proof offered is its own state. "Nothing was checked" must not look
//    like "checked and fine", and must not look like "checked and failed".
LOOKUP = NOPROOF;
await loadRecord(P); await settle();
const none = document.getElementById('proofpanel').innerHTML;
if (!none.includes('no inclusion proof'))
  throw new Error('a missing proof was not reported: ' + none.slice(0, 500));
if (none.includes('checked in your browser') || none.includes('FAILED'))
  throw new Error('a missing proof was reported as a verdict: ' + none.slice(0, 500));

// 4. Every state the reader is asked to act on carries a WORD, not just a
//    colour. --ok green and --bad red are nearly the same luminance, so a page
//    that distinguishes them by hue alone says nothing in greyscale.
LOOKUP = GOOD;
await loadRecord(P); await settle();
const detail = document.getElementById('detail').innerHTML;
if (!/pill-(ok|bad)/.test(detail)) throw new Error('the state pill is missing from the record detail');
// Strip entities BEFORE looking for a word. The first version of this check
// tested the raw text, and "&nbsp;" satisfied /[a-z]/ on the strength of the
// entity name — so a pill with no visible word at all passed. The assertion
// has to be about what a reader sees.
const pills = [...detail.matchAll(/<span class="pill[^"]*">([^<]*)</g)].map(m => m[1]);
if (!pills.length) throw new Error('no pills rendered at all, so this proves nothing');
for (const p of pills) {
  const visible = p.replace(/&[a-zA-Z]+;|&#\d+;/g, '').trim();
  if (!/[a-z]/i.test(visible))
    throw new Error('a pill carries no word a reader can see, only colour: ' + JSON.stringify(p));
}

// 5. The history table lists the versions the API actually returned, and does
//    not invent a "what changed" column the endpoint cannot back.
const hist = document.getElementById('history').innerHTML;
const want = JSON.parse(VERSIONS).data.versions || [];
if (!want.length) throw new Error('the fixture has no versions, so this proves nothing');
for (const v of want) {
  if (!hist.includes(v)) throw new Error('version ' + v + ' missing from the history table: ' + hist.slice(0, 400));
}
if (/what changed/i.test(hist)) throw new Error('the history table claims a diff the API cannot back');

console.log('BROWSE-JS-OK');
`

	script := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(script, []byte(harness+string(vjs)+pageJS+checks), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", script,
		filepath.Join(dir, "good.json"), filepath.Join(dir, "bad.json"),
		filepath.Join(dir, "noproof.json"), filepath.Join(dir, "versions.json"),
		filepath.Join(dir, "ckpt.txt"), vkey)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "BROWSE-JS-OK") {
		t.Fatalf("browse page JS: %v\n%s", err, out)
	}
}
