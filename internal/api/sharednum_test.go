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
	checks := `
// Every branch of the rendered line, not of ago(): the bug was in the caller,
// and ago() alone was always correct.
console.log(JSON.stringify([null, -1, 1, 44, 120, 4000, 90000, 1036800].map(checkpointLine)));
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
	if len(got) < 8 {
		t.Fatalf("only %d states rendered", len(got))
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
