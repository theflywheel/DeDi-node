package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A verdict that states no result must never render as a clean one.
//
// This page fans out to peers and draws THEIR /dedi/witness responses. Every
// surface tested only for the failure case — `consistency_ok === false` — so a
// verdict arriving with the field absent fell through to the affirmative
// branch: a solid arrow, a ✓ badge, and a green "verified" cell over a bit
// nobody had asserted.
//
// It matters more here than on the pages that had the same bug before, because
// these bytes come from another party over the network. Omitting a JSON field
// is not an attack that takes effort — an older build does it by accident.
//
// The shapes below are the real ones: what this node's own witnessTargetView
// puts on the wire, and the same minus the field a peer might not send.
func TestNetworkWillNotDrawAnUnstatedVerdictAsClean(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "network.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	js := src[strings.LastIndex(src, "<script>")+len("<script>"):]
	js = js[:strings.Index(js, "</script>")]
	if !strings.Contains(src, "/static/verdict.js") {
		t.Fatal("the page no longer loads the shared verdict classifier; it or this test has moved")
	}
	// The classifier is loaded, not copied. Prepending it here is what the
	// browser does with the <script src>.
	shared, err := os.ReadFile(filepath.Join("static", "verdict.js"))
	if err != nil {
		t.Fatal(err)
	}
	js = string(shared) + js

	// The wire shape this node really produces for a sound verdict.
	srv, s, _ := testServer(t)
	seedVerdict(t, s, "target.example/log", soundVerdict(4), "live")
	body := mustGetBodyOf(t, srv.URL+"/dedi/witness")
	var env struct {
		Data struct {
			Targets []map[string]any `json:"targets"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Targets) != 1 {
		t.Fatalf("fixture produced %d targets, want 1", len(env.Data.Targets))
	}
	sound := env.Data.Targets[0]
	if sound["consistency_ok"] != true || sound["witnessed"] != true {
		t.Fatalf("the fixture is not a sound verdict: %v", sound)
	}
	// Health is supplied by a hook this bare server has none of, so add one —
	// its absence is a separate case, exercised below.
	sound["health"] = map[string]any{"checking": true, "stale": false}

	variant := func(mutate func(map[string]any)) string {
		c := map[string]any{}
		for k, v := range sound {
			c[k] = v
		}
		mutate(c)
		b, _ := json.Marshal(c)
		return string(b)
	}
	cases := map[string]string{
		// health as a truthy non-object: the classifier type-checks
		// consistency_ok and a truthiness guard on the very next line would
		// hand this one straight back to the affirmative branch.
		"healthstring": variant(func(m map[string]any) { m["health"] = "yes" }),
		"healthtrue":   variant(func(m map[string]any) { m["health"] = true }),
		// a verdict that names no tree size printed "✓ NaN" under a green tick
		"nosize": variant(func(m map[string]any) { delete(m, "size") }),
		"sound":     variant(func(m map[string]any) {}),
		"absent":    variant(func(m map[string]any) { delete(m, "consistency_ok") }),
		"string":    variant(func(m map[string]any) { m["consistency_ok"] = "false" }),
		"nohealth":  variant(func(m map[string]any) { delete(m, "health") }),
		"caught":    variant(func(m map[string]any) { m["consistency_ok"] = false }),
		"nowitness": variant(func(m map[string]any) { m["witnessed"] = false }),
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
for (const [name, raw] of Object.entries(CASES)) {
  const t = JSON.parse(raw);
  out[name] = {
    cls: edgeClass(t),
    label: edgeLabel(t),
    panel: verdictTable(t).replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim(),
    health: healthNote(t).replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim(),
  };
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
	var got map[string]struct{ Cls, Label, Panel, Health string }
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}

	// The affirmative rendering belongs to exactly one case.
	if c := got["sound"]; c.Cls != "" || !strings.Contains(c.Label, "✓") ||
		!strings.Contains(c.Panel, "verified") {
		t.Fatalf("the sound verdict no longer renders as sound (%+v); this test proves nothing", c)
	}
	for _, name := range []string{"absent", "string", "nohealth", "healthstring", "healthtrue", "nosize", "caught", "nowitness"} {
		c := got[name]
		if c.Cls == "" {
			t.Errorf("%s: drawn with the same class as a sound verdict", name)
		}
		if strings.HasPrefix(c.Label, "✓") {
			t.Errorf("%s: badged %q — a tick over a result nobody asserted", name, c.Label)
		}
	}
	// And each unclean case must say, in words, which it is.
	if p := got["absent"].Panel; !strings.Contains(p, "does not state a result") {
		t.Errorf("a verdict with no result reads as: %q", p)
	}
	if p := got["string"].Panel; !strings.Contains(p, "does not state a result") {
		t.Errorf("a non-boolean result reads as: %q", p)
	}
	for _, name := range []string{"nohealth", "healthstring", "healthtrue"} {
		if h := got[name].Health; !strings.Contains(h, "reports nothing") {
			t.Errorf("%s: a verdict with no readable liveness carries no caveat: %q", name, h)
		}
	}
	// A number the page cannot state must never be drawn at all.
	for name, c := range got {
		if strings.Contains(c.Label, "NaN") || strings.Contains(c.Panel, "NaN") {
			t.Errorf("%s: renders NaN — %q / %q", name, c.Label, c.Panel)
		}
	}
}

// Witnessing and replication must stay distinguishable by SHAPE, not shade.
//
// The canvas annotation is emphatic and the reason is arithmetic: --ok, --bad
// and --wit sit at nearly the same luminance, so a reader without colour — in
// greyscale, in print, or colourblind — has only words and layout to go on.
// Witnessing is arrows BETWEEN boxes; replication is copies INSIDE one hatched
// boundary. Three replicas agreeing is one party speaking three times, and if
// that ever reads as "three parties verified" the page has taught the exact
// opposite of what it exists to teach.
func TestReplicationCannotBeReadAsWitnessing(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")
	page := mustGetBodyOf(t, srv.URL+"/network")

	// The replication block is prose plus member cards. It must not borrow the
	// vocabulary of a verdict.
	i := strings.Index(page, "one identity")
	if i < 0 {
		t.Fatal("the replication boundary's eyebrow is gone; it or this test has moved")
	}
	// Just the sentence that heads the boundary and the member rendering below
	// it. An earlier version of this test took everything up to the next <h2>,
	// which in a page whose markup is built in JS swallowed unrelated code and
	// flagged two correct uses: the coverage message elsewhere, and the Raft
	// quorum sentence — a majority genuinely must AGREE about ordering, and
	// banning the word there would have been the test being wrong about the
	// domain rather than the page being wrong about the claim.
	block := page[i:]
	if j := strings.Index(block, "lag_entries"); j > 0 {
		block = block[:j]
	}
	// A verdict's vocabulary is what must not appear: a replica is not a party
	// that verified anything.
	for _, borrowed := range []string{"pill-ok", "pill-wit", "verified", "independently"} {
		if strings.Contains(strings.ToLower(block), borrowed) {
			t.Errorf("the replication boundary uses %q, which is the vocabulary of a witness verdict; "+
				"three copies of one log are not three parties verifying it", borrowed)
		}
	}
	// And it has to say what it is, in a word, not by its hatching alone.
	if !strings.Contains(strings.ToLower(block), "cop") {
		t.Error("the replication boundary never uses the word 'copies', so only its shading says " +
			"what it is")
	}
	// The page as a whole must keep saying the thing outright.
	if !strings.Contains(page, "three parties verified") {
		t.Error("the page no longer names the misreading it exists to prevent")
	}
}

// No page may conclude a verdict is sound from the absence of a failure.
//
// This bug reached five pages because each open-coded the same test as
// "not explicitly false", and the rule for reading a verdict lived in a comment
// on one page the others never saw.
//
// The rule this asserts is the defect, not a house style. Testing
// `consistency_ok === true` is CORRECT and two pages already did it — an
// earlier version of this test banned that spelling too and flagged both of
// them, which was the test being wrong about the domain rather than the pages
// being wrong about verdicts. What must never appear is soundness inferred
// from `!== false`, and any page drawing a FULL verdict — where liveness and
// tree size also decide what may be claimed — must ask the shared classifier
// rather than assemble its own.
func TestNoPageInfersASoundVerdictFromTheAbsenceOfFailure(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("found no pages, so this proves nothing")
	}
	// The pages that render a verdict as a whole, rather than one field of one.
	full := map[string]bool{"network.html": true, "status.html": true, "overview.html": true}
	checked := 0
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := withoutComments(string(b))
		if !strings.Contains(src, "consistency_ok") {
			continue
		}
		checked++
		name := filepath.Base(p)
		if strings.Contains(src, "consistency_ok !== false") ||
			strings.Contains(src, "consistency_ok != false") {
			t.Errorf("%s treats a verdict that is not explicitly a failure as a sound one", name)
		}
		if full[name] && !strings.Contains(src, "/static/verdict.js") {
			t.Errorf("%s draws whole verdicts without the shared classifier, so its reading of "+
				"liveness and tree size can drift from every other page's", name)
		}
	}
	if checked == 0 {
		t.Fatal("no page reads consistency_ok, so this proves nothing")
	}
}
