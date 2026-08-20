package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// A signature that could not be checked must never read as one that passed.
//
// verifyCheckpointSig returns null when there is no key — neither true nor
// false. The banner cleared its all-clear flag only on an explicit false, so a
// node publishing no verifier key rendered amber "skip" chips and then opened
// with a green "this node's witness claim holds and nothing was taken on its
// word". Both halves were false, and the step-4 chip could read a green
// "only appended since" while resting on an unauthenticated checkpoint.
//
// This is the fourth page to carry that shape, so the assertion is on the words
// a reader sees, not on a class name: a class can be renamed, and the claim is
// the thing that must not be made.
func TestVerifyWillNotCallAnUncheckedSignatureAPass(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "verify.html"))
	if err != nil {
		t.Fatal(err)
	}
	vjs, err := os.ReadFile(filepath.Join("static", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	// The program script, which follows the verify.js include — not the first
	// <script> on the page and not any later mention of the word in a comment.
	inc := strings.Index(src, "verify.js")
	if inc < 0 {
		t.Fatal("the page no longer includes verify.js; it or this test has moved")
	}
	after := src[inc:]
	open := strings.Index(after, "<script>")
	if open < 0 {
		t.Fatal("no program script after the verify.js include")
	}
	js := after[open+len("<script>"):]
	end := strings.Index(js, "</script>")
	if end < 0 {
		t.Fatal("the program script is unterminated")
	}
	js = js[:end]
	if !strings.Contains(js, "skips.push") {
		t.Fatal("the page no longer tracks skipped checks; it or this test has moved")
	}

	// A node that actually witnesses another, because the page returns early
	// when it witnesses nobody and would then have no verdict to report at all.
	//
	// The target is a stub rather than a second node: two stores on one test
	// database collide. What matters is that the bytes it serves are real —
	// a genuinely signed checkpoint note and a genuine consistency proof — so
	// the page's own crypto does the deciding.
	srv, st, vkey := testServer(t)
	seedBasic(t, st) // a non-empty log, so step 4 is a real check and not "not yet applicable"
	fx := verifyFixtures(t, srv, st)
	dir := t.TempDir()
	fxPath := filepath.Join(dir, "fx.json")
	if err := os.WriteFile(fxPath, fx, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := `
import fs from 'node:fs';
if (!globalThis.crypto) Object.defineProperty(globalThis, 'crypto',
  { value: (await import('node:crypto')).webcrypto, configurable: true });
const FX = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const BASE = process.argv[3];
const KEY = process.argv[4];
for (const k of Object.keys(FX)) FX[k] = FX[k].split("__KEY__").join(KEY);
const els = new Map();
const mk = id => ({ id, innerHTML: '', textContent: '', style: {}, className: '',
  addEventListener(){}, scrollIntoView(){}, querySelectorAll(){ return []; }, appendChild(){} });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, mk(id)); return els.get(id); },
  querySelector: s => s && s.startsWith('meta') ? { content: KEY } : null,
  querySelectorAll: () => [], createElement: () => mk('e'), addEventListener(){},
};
globalThis.location = { origin: BASE, hash: '' };
globalThis.addEventListener = () => {};
globalThis.setInterval = () => {};
globalThis.matchMedia = () => ({ matches: true });
// Every response is a fixture the Go side produced from the real checkpointer,
// the real witness records and the real proof endpoints. Nothing is invented
// here: the page's own crypto decides, over bytes the node actually signs.
globalThis.fetch = async (u) => {
  const key = Object.keys(FX).find(k => u === k || u.endsWith(k));
  if (key === undefined) return { ok: false, status: 404, text: async () => '', json: async () => ({}) };
  const body = FX[key];
  return { ok: true, status: 200, text: async () => body, json: async () => JSON.parse(body) };
};
`

	checks := `
await run().catch(e => { console.log('RUN-ERROR: ' + e.message); process.exit(2); });
const out = document.getElementById('out').innerHTML;
const banner = out.slice(0, out.indexOf('class="steps"') + 1) || out.slice(0, 1500);
const holds = /witness claim holds/.test(banner);
const reason = (banner.match(/<li>([\s\S]*?)<\/li>/g) || []).map(x => x.replace(/<[^>]*>/g,'')).join(' | ');
const partly = /Partly checked/.test(banner);
const skipChip = /class="chip skip"/.test(out);
const chips=[...out.matchAll(/<div class="chip (pass|fail|skip)"[^>]*>([^<]*)/g)].map(m=>m[1]+":"+m[2].replace(/&[a-z]+;/g,""));
console.log(JSON.stringify({ holds, partly, skipChip, reason, chips, banner: banner.replace(/<[^>]*>/g,' ').replace(/\s+/g,' ').slice(0,220) }));
`
	run := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(run, []byte(harness+string(vjs)+js+checks), 0o600); err != nil {
		t.Fatal(err)
	}

	exec1 := func(key string) verdict { return runPage(t, run, fxPath, srv.URL, key) }

	// No key: the signature check cannot run.
	no := exec1("")
	// With every key present no step may be left unperformed, or the banner's
	// "All checks recomputed" is printed over one that was.
	if !no.SkipChip {
		t.Fatal("with no verifier key nothing was reported as skipped; this test proves nothing")
	}
	if no.Holds {
		t.Error("with no key to check it against, the page still claims the witness claim holds " +
			"and that nothing was taken on the node's word — neither is true")
	}
	if !no.Partly {
		t.Error("a run with an unchecked signature does not say it was only partly checked")
	}

	// With the node's real key, the same run must still be able to say it holds.
	yes := exec1(vkey)
	if yes.SkipChip {
		t.Error("with every key present a step still reports itself unperformed; " +
			"the with-key case is no longer exercising all four steps")
	}
	if yes.Partly {
		t.Errorf("a fully checked run reports itself as only partly checked")
	}
	if !yes.Holds {
		t.Error("a run with every check performed no longer states its conclusion")
	}

	// A step that is "not yet applicable" is still a step that did not run.
	//
	// The append-only check has nothing to compare when the target's log was
	// empty at the moment it was first witnessed. With both keys present, every
	// signature verifies — so tracking only the signature checks left that step
	// reporting itself unperformed underneath an "All checks recomputed"
	// headline. The same overclaim, one element to the right.
	srv2, st2, vkey2 := testServer(t)
	fxZero := verifyFixturesAt(t, srv2, st2, true)
	zeroPath := filepath.Join(dir, "fx-zero.json")
	if err := os.WriteFile(zeroPath, fxZero, 0o600); err != nil {
		t.Fatal(err)
	}
	z := runPage(t, run, zeroPath, srv2.URL, vkey2)
	if !z.SkipChip {
		t.Fatal("the empty-log fixture did not produce an unperformed step; it proves nothing")
	}
	if z.Holds {
		t.Error("every key verified, but one step could not run — and the page still says all " +
			"checks were recomputed and nothing was taken on the node's word")
	}
	if !z.Partly {
		t.Error("a run with an unperformed step does not say it was only partly checked")
	}
	// And it must say WHY that step could not run. Both signatures verified
	// here, so explaining the partial result as a missing key would be telling
	// the reader something false about the one thing they came to check.
	if strings.Contains(z.Reason, "verifier key") || strings.Contains(z.Reason, "no key is published") {
		t.Errorf("the empty-log case is explained as a missing key, which it is not: %q", z.Reason)
	}
	if !strings.Contains(z.Reason, "empty when it was first witnessed") {
		t.Errorf("the empty-log case does not say why the step could not run: %q", z.Reason)
	}

	// The missing-key case must give its own reason, not the empty-log one.
	if !strings.Contains(no.Reason, "key") {
		t.Errorf("a run with no keys does not say a key was missing: %q", no.Reason)
	}
}

// verifyFixtures captures exactly the responses /verify fetches, produced by
// this node's real endpoints, so the page under test folds bytes the node
// actually signed rather than bytes a test author typed.
func verifyFixtures(t *testing.T, srv *httptest.Server, s *store.Store) []byte {
	return verifyFixturesAt(t, srv, s, false)
}

// verifyFixturesAt builds the fixture set, optionally recording the verdict at
// size 0 — the "witnessed while the target's log was still empty" case, where
// step 4 has nothing to compare and reports itself unperformed.
// The verifier key is deliberately absent here: the fixtures are the node's
// bytes, and which key the page checks them with is the variable under test.
func verifyFixturesAt(t *testing.T, srv *httptest.Server, s *store.Store, atZero bool) []byte {
	t.Helper()
	const targetOrigin = "target.example/log"

	// The verdict records the size and root of the very checkpoint served as the
	// target's, so step 4's unchanged-tree branch holds without a consistency
	// proof. Taken from the real note rather than typed: a hand-written root
	// would make step 4 fail and mask whatever the signature steps did.
	cpNote := mustGetBodyOf(t, srv.URL+"/dedi/log/checkpoint")
	size, rootB64 := parseNoteSizeRoot(t, cpNote)
	if atZero {
		size, rootB64 = 0, ""
	}
	verdict, _ := json.Marshal(map[string]any{
		"target": "https://" + targetOrigin, "size": size, "root": rootB64,
		"consistency_ok": true,
	})
	seedVerdict(t, s, targetOrigin, string(verdict), "live")

	fx := map[string]string{}
	grab := func(path string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d — %s", path, resp.StatusCode, b)
		}
		fx[path] = string(b)
	}
	grab("/dedi/log/checkpoint")
	grab("/dedi/witness")
	grab("/dedi/witness/" + url.PathEscape(targetOrigin))
	grab("/dedi/lookup/_witness/" + url.PathEscape(targetOrigin) + "/checkpoint?proof=inclusion&internal=1")

	// The network view has to say this node witnesses someone, or the page
	// returns early and there is no verdict to check at all.
	var net map[string]any
	if err := json.Unmarshal([]byte(fx["/dedi/witness"]), &net); err != nil {
		t.Fatal(err)
	}
	netJSON, _ := json.Marshal(map[string]any{"data": map[string]any{
		"witnessing":  targetOrigin,
		"witness_url": "https://" + targetOrigin,
		"witness_key": "__KEY__",
		"nodes": []map[string]any{
			{"name": "this node", "self": true, "reachable": true, "origin": "test.dedi.local/log"},
		},
		"total": 1, "reachable": 1,
	}})
	fx["/dedi/network"] = string(netJSON)

	blob, err := json.Marshal(fx)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

type verdict struct {
	Holds, Partly, SkipChip bool
	Reason                  string
}

func runPage(t *testing.T, script, fx, base, key string) verdict {
	t.Helper()
	out, err := exec.Command("node", script, fx, base, key).CombinedOutput()
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("page did not report a verdict: %v\n%s", err, out)
	}
	var v verdict
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustGetBodyOf(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// parseNoteSizeRoot reads the size and root out of a signed checkpoint note,
// the same two lines the page parses.
func parseNoteSizeRoot(t *testing.T, note string) (int64, string) {
	t.Helper()
	lines := strings.Split(note, "\n")
	if len(lines) < 3 {
		t.Fatalf("checkpoint note has %d lines, want at least 3", len(lines))
	}
	n, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		t.Fatalf("checkpoint size %q: %v", lines[1], err)
	}
	return n, strings.TrimSpace(lines[2])
}

// The page tells a reader to check the node themselves. That instruction has to
// work.
//
// The caveat panel points at /dedi/log/proof/consistency and originally said it
// accepts any saved starting size. It does not: logConsistency rejects old < 1.
// A reader who saved a checkpoint of an empty log would have followed the
// instruction and received a 400 — in exactly the empty-log case this page
// treats as only partly checked.
//
// I had "verified" that claim by finding where the handler parses `old`, which
// showed the parameter is read and not what values it accepts. The bound is on
// the next line. So this asserts the page's promise against the endpoint's real
// behaviour rather than against a reading of it.
func TestVerifyPromisesOnlyWhatTheProofEndpointDoes(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)

	page, err := os.ReadFile(filepath.Join("static", "verify.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "/dedi/log/proof/consistency") {
		t.Skip("the page no longer points readers at the consistency endpoint")
	}

	ask := func(old, nu string) int {
		t.Helper()
		resp, err := http.Get(srv.URL + "/dedi/log/proof/consistency?old=" + old + "&new=" + nu)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	size := fmt.Sprint(currentTreeSize(t, srv))

	// The promise the page makes: a size you saved works.
	if got := ask("1", size); got != http.StatusOK {
		t.Errorf("a proof from size 1 to %s: %d, but the page tells readers to ask for one", size, got)
	}
	// The bound the page must not promise past.
	//
	// Loosening logConsistency's own `old < 1` does not make this pass: the
	// store's ProveConsistency rejects 0 as well, so the request turns into a
	// 500 rather than a proof. The property is defended in two layers, which is
	// why this assertion cannot be tripped by relaxing only the handler — not
	// because it checks nothing.
	if got := ask("0", size); got == http.StatusOK {
		t.Error("size 0 is accepted after all; the caveat's carve-out is now wrong in the other direction")
	}
	// And the page must say so.
	if !strings.Contains(string(page), "not empty when you saved it") {
		t.Error("the page does not tell readers that an empty starting tree cannot be proved from")
	}
}

func currentTreeSize(t *testing.T, srv *httptest.Server) int64 {
	t.Helper()
	note := mustGetBodyOf(t, srv.URL+"/dedi/log/checkpoint")
	n, _ := parseNoteSizeRoot(t, note)
	return n
}
