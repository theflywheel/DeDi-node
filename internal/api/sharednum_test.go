package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every page that prints a quantity defined its own `num`, all four identical
// and none of them guarded, so a field the node did not send rendered as the
// literal string "NaN" under that field's own label on every one of them.
//
// The formatter is now in the shared file, and this test fails any page that
// takes a local copy again — which is the only way the bug comes back.
// const, let, var and a function declaration, with or without the space a
// formatter happens to leave: a guard that knows two spellings stops the two
// it knows.
var localNum = regexp.MustCompile(`(?:const|let|var)\s+num\s*=|function\s+num\s*\(`)

func TestNoPageKeepsItsOwnNumberFormatter(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no pages found: %v", err)
	}
	prints := 0
	for _, p := range pages {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		if !strings.Contains(src, "num(") {
			continue
		}
		prints++
		name := filepath.Base(p)
		if localNum.MatchString(src) {
			t.Errorf("%s defines its own number formatter; that is how the NaN bug reached four pages", name)
		}
		if !strings.Contains(src, "/static/verdict.js") {
			t.Errorf("%s calls num() without loading the file that defines it", name)
		}
	}
	// If the glob or the call shape changes, the loop above passes by finding
	// nothing. It has to have actually looked at some pages.
	if prints < 3 {
		t.Fatalf("only %d pages appear to print quantities; this test has stopped looking at them", prints)
	}
}

// The formatter's whole job is the distinction between a number and an absent
// value, so it is tested on both rather than assumed from its source.
func TestTheSharedFormatterRefusesToPrintAnAbsentValueAsAQuantity(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	shared, err := os.ReadFile(filepath.Join("static", "verdict.js"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := filepath.Join(dir, "run.mjs")
	script := string(shared) + `
const out = {};
for (const [name, v] of Object.entries({
  whole: 4210, zero: 0, negative: -3, float: 1.5,
  absent: undefined, nul: null, empty: '', text: 'six', nan: NaN, infinite: Infinity,
  numericString: '4210',
})) out[name] = num(v);
console.log(JSON.stringify(out));
`
	if err := os.WriteFile(run, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("node", run).CombinedOutput()
	line := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("the formatter reported nothing: %v\n%s", err, raw)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	const dash = "—"
	// Nothing that is not a number may render as one -- least of all as "NaN",
	// which reads as a value rather than as an absence.
	for _, name := range []string{"absent", "nul", "empty", "text", "nan", "infinite"} {
		if got[name] != dash {
			t.Errorf("%s rendered as %q; a value the node did not send is not a quantity", name, got[name])
		}
	}
	// ...and a real number must still be printed, including the two that are
	// falsy and would be swallowed by a truthiness guard.
	for name, want := range map[string]string{
		"whole": "4,210", "zero": "0", "negative": "-3", "float": "1.5", "numericString": "4,210",
	} {
		if got[name] != want {
			t.Errorf("%s rendered as %q, want %q", name, got[name], want)
		}
	}
}

// The front door renders a checkpoint age by calling ago(), which already ends
// its own string with the word. Two branches appended " ago" to it anyway, so
// every load with a signed checkpoint said "44 seconds ago ago" — on the first
// line of the first page anyone sees.
//
// Nothing caught it because every test read the page's SOURCE. This one runs
// the page's own function and reads what a person would see.
func TestThePageDoesNotSayAgoTwice(t *testing.T) {
	// Every branch of the rendered line, not of ago(): the bug was in the
	// caller, and ago() alone was always correct. The last three ages carry
	// unsigned entries, which is the only way to reach the stale branch.
	got := runOverviewJS(t, `
console.log(JSON.stringify([
  ...[null, -1, 1, 44, 120].map(a => checkpointLine(a, false)),
  ...[4000, 90000, 1036800].map(a => checkpointLine(a, true)),
]));
`)
	if len(got) < 8 {
		t.Fatalf("only %d states rendered", len(got))
	}
	if !strings.Contains(got[5], `class="warn"`) {
		t.Fatalf("the stale branch was not reached: %q", got[5])
	}
	words := 0
	for _, s := range got {
		if n := strings.Count(s, "ago"); n > 1 {
			t.Errorf("%q says ago %d times", s, n)
		}
		// Every state carries a word, not only a colour: --ok and --warn are
		//0.05 apart in luminance and identical in greyscale.
		text := stripTags(s)
		if strings.TrimSpace(text) == "" {
			t.Errorf("a checkpoint state renders with no words at all: %q", s)
		}
		if strings.Contains(text, "ago") {
			words++
		}
	}
	// The three ages, and only those, read as ages -- unknown and never-signed
	// must not acquire one.
	if words != 6 {
		t.Errorf("%d of %d states read as an age; want the 6 real ages: %q", words, len(got), got)
	}
	for _, s := range []string{got[0], got[1]} {
		if strings.Contains(s, "ago") {
			t.Errorf("a checkpoint with no age reads as one: %q", s)
		}
	}
}

// The checkpointer signs only when the tree grows, so a quiet node's newest
// checkpoint is legitimately old — and once witness verdicts stopped arriving
// every minute, quiet became the normal state. The front door used to call any
// checkpoint over an hour old stale, which would have painted every idle node
// amber. It must decide by position, as /status and the alert rule do: stale
// only when entries sit above the signed size. This drives the page's real
// /metrics read, not just the line renderer, so a wrong metric name fails too.
func TestTheFrontDoorDoesNotCallAQuietLogStale(t *testing.T) {
	got := runOverviewJS(t, `
const scrape = (age, entries, signed) =>
  '# HELP dedi_log_entries x\n# TYPE dedi_log_entries gauge\n' +
  'dedi_log_entries ' + entries + '\ndedi_checkpoint_tree_size ' + signed +
  '\ndedi_checkpoint_age_seconds ' + age + '\n';
(async () => {
  const out = [];
  for (const m of [
    scrape('1.0368e+06', 22043, 22043),  // quiet for twelve days, everything signed
    scrape(7200, 22050, 22043),           // seven entries no signature covers
    scrape(30, 22050, 22043),             // just written; the next tick signs it
  ]) {
    globalThis.fetch = async () => ({ ok: true, text: async () => m });
    out.push(await checkpointCell());
  }
  console.log(JSON.stringify(out));
})();
`)
	if len(got) != 3 {
		t.Fatalf("rendered %d lines, want 3: %q", len(got), got)
	}
	if strings.Contains(got[0], "warn") || !strings.Contains(got[0], `class="ok"`) {
		t.Errorf("a quiet, fully signed log reads as a fault: %q", got[0])
	}
	if !strings.Contains(got[1], `class="warn"`) || !strings.Contains(stripTags(got[1]), "unsigned") {
		t.Errorf("entries above an hour-old signature are not flagged: %q", got[1])
	}
	if strings.Contains(got[2], "warn") {
		t.Errorf("a write awaiting its next signing tick is flagged: %q", got[2])
	}
}

// runOverviewJS runs overview.html's script, with verdict.js before it as the
// page loads it, then the given checks, and returns the last line the checks
// printed as a JSON array of strings.
func runOverviewJS(t *testing.T, checks string) []string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "overview.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	js := src[strings.LastIndex(src, "<script>")+len("<script>"):]
	js = js[:strings.Index(js, "</script>")]
	shared, err := os.ReadFile(filepath.Join("static", "verdict.js"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	run := filepath.Join(dir, "run.mjs")
	harness := `
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
`
	if err := os.WriteFile(run, []byte(harness+string(shared)+js+checks), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("node", run).CombinedOutput()
	line := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "[") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("the page reported nothing: %v\n%s", err, raw)
	}
	var got []string
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// A page that uses a state class must be served a stylesheet that defines it.
// check.html once styled a callout with var(--line) and var(--warnbg), which
// were verify.html's locals — the rule parsed, matched, and painted nothing.
// The same shape reappeared here: overview.html marked a stale checkpoint
// .warn while declaring only .ok, .no and .mut.
func TestEveryStateClassAPageUsesIsActuallyDefined(t *testing.T) {
	shared, err := os.ReadFile(filepath.Join("static", "components.css"))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no pages: %v", err)
	}
	uses := regexp.MustCompile(`class="(?:[^"]*\s)?(ok|no|mut|warn)(?:\s[^"]*)?"`)
	checked := 0
	for _, p := range pages {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(body)
		found := map[string]bool{}
		for _, m := range uses.FindAllStringSubmatch(src, -1) {
			found[m[1]] = true
		}
		if len(found) == 0 {
			continue
		}
		checked++
		for class := range found {
			rule := "." + class + " {"
			if !strings.Contains(string(shared), rule) && !strings.Contains(src, rule) {
				t.Errorf("%s marks something %q and nothing defines that class",
					filepath.Base(p), class)
			}
		}
	}
	if checked < 4 {
		t.Fatalf("only %d pages appear to use state classes; this test has stopped looking", checked)
	}
}
