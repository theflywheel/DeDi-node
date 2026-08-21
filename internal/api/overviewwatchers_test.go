package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A watcher that has verified this log and then gone quiet has produced a
// verdict. A watcher that has never verified it has not. The overview folded
// both into "there is no verdict to read", which states a false reason for the
// caveat: the verdict exists, it passed, and what is missing is evidence the
// watcher is still watching.
//
// This runs the page's real renderWatchers over the real classifier rather
// than asserting on source text, so a branch that stops distinguishing the two
// fails here whatever it is rewritten to look like.
func TestOverviewSeparatesAStaleVerdictFromNoVerdict(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "overview.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	if !strings.Contains(src, "/static/verdict.js") {
		t.Fatal("the overview no longer loads the shared verdict classifier; it or this test has moved")
	}
	js := src[strings.LastIndex(src, "<script>")+len("<script>"):]
	js = js[:strings.Index(js, "</script>")]
	shared, err := os.ReadFile(filepath.Join("static", "verdict.js"))
	if err != nil {
		t.Fatal(err)
	}
	js = string(shared) + js

	// Built from the classifier's own contract, one field at a time, so each
	// case differs from `sound` only in the thing it is named for.
	cases := map[string]map[string]any{
		"sound": {
			"witnessed": true, "consistency_ok": true, "size": 41,
			"verdict_at": "2026-08-20T10:00:00Z",
			"health":     map[string]any{"stale": false, "checking": true},
		},
		"stale": {
			"witnessed": true, "consistency_ok": true, "size": 41,
			"verdict_at": "2026-08-20T10:00:00Z",
			"health":     map[string]any{"stale": true, "checking": true},
		},
		"unwatched": {
			"witnessed": true, "consistency_ok": true, "size": 41,
			"verdict_at": "2026-08-20T10:00:00Z",
		},
		"never": {"witnessed": false},
		"unstated": {
			"witnessed": true, "verdict_at": "2026-08-20T10:00:00Z",
		},
	}
	blob, _ := json.Marshal(cases)
	dir := t.TempDir()
	fx := filepath.Join(dir, "cases.json")
	if err := os.WriteFile(fx, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := `
import fs from 'node:fs';
const els = new Map();
const mk = id => ({ id, innerHTML: '', textContent: '', style: {}, className: '',
  addEventListener(){}, appendChild(){}, querySelectorAll(){ return []; } });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, mk(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [], createElement: () => mk('e'),
  addEventListener(){},
};
globalThis.location = { origin: 'http://node.example', protocol: 'http:', hash: '' };
globalThis.addEventListener = () => {};
globalThis.setInterval = () => {};
globalThis.fetch = async () => ({ ok: true, json: async () => ({data:{}}), text: async () => '' });
const CASES = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
`
	checks := `
const out = {};
for (const [name, t] of Object.entries(CASES)) {
  renderWatchers({ found: [{ node: 'w1', t }] });
  out[name] = document.getElementById('watchers').innerHTML
    .replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
}
console.log(JSON.stringify(out));
`
	run := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(run, []byte(harness+js+checks), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("node", run, fx).CombinedOutput()
	line := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("the page reported nothing: %v\n%s", err, raw)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}

	const noVerdict = "no verdict to read"
	// The two states that HAVE a verdict must report the size it was reached
	// at, and must not tell the reader there is nothing to read.
	for _, name := range []string{"sound", "stale", "unwatched"} {
		s := got[name]
		if !strings.Contains(s, "41") {
			t.Errorf("%s: a verdict was reached at 41 entries and the page does not say so:\n%s", name, s)
		}
		if strings.Contains(s, noVerdict) {
			t.Errorf("%s: the page says %q about a verdict that exists and passed:\n%s", name, noVerdict, s)
		}
	}
	// ...and the two that have none must not print an entry count at all.
	for _, name := range []string{"never", "unstated"} {
		s := got[name]
		if !strings.Contains(s, noVerdict) {
			t.Errorf("%s: no verdict exists and the page does not say so:\n%s", name, s)
		}
	}
	// The caveat has to be legible as a caveat, not merely absent-of-claim:
	// a stale verdict that reads identically to a live one is the false green
	// this whole change removes.
	for _, name := range []string{"stale", "unwatched"} {
		if got[name] == got["sound"] {
			t.Errorf("%s renders exactly like a current verdict:\n%s", name, got[name])
		}
	}
	if !strings.Contains(got["stale"], "stale") {
		t.Errorf("a stale verdict does not carry the word:\n%s", got["stale"])
	}
	if !strings.Contains(got["unwatched"], "still checking") {
		t.Errorf("a verdict with no liveness report does not say what is missing:\n%s", got["unwatched"])
	}
}

// The copy button reported success whenever the clipboard API was absent —
// which is the normal case over plain http, exactly where this page is likely
// to be run. A control that says it did something it did not do is the same
// false affirmative the rest of this change removes, in miniature.
func TestTheCopyButtonDoesNotClaimACopyItDidNotMake(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("static", "network.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	i := strings.Index(src, "ck.onclick")
	if i < 0 {
		t.Fatal("the copy handler has moved; this test no longer reads it")
	}
	h := src[i:]
	if j := strings.Index(h, "\n    };"); j > 0 {
		h = h[:j]
	}
	// `done` is the branch that prints "copied". It may be reached only from a
	// resolved writeText, never from the no-clipboard fallback.
	if !strings.Contains(h, "writeText(k).then(done") {
		t.Errorf("success is no longer tied to a resolved write:\n%s", h)
	}
	if strings.Contains(h, "else done()") {
		t.Errorf("the no-clipboard path still reports a successful copy:\n%s", h)
	}
	if !strings.Contains(h, "else failed()") {
		t.Errorf("the no-clipboard path does not tell the reader the copy did not happen:\n%s", h)
	}
	// A rejected write must land somewhere that says so, not be swallowed.
	if strings.Contains(h, "then(done, () => {})") {
		t.Errorf("a rejected write is still discarded silently:\n%s", h)
	}
}
