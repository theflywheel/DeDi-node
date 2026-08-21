package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every page that prints a quantity used to define its own `num`, and every one
// of them rendered a field the node did not send as the literal string "NaN"
// under that field's own label. It was found and fixed one page at a time.
//
// The formatter is now in the shared file, and this test fails any page that
// takes a local copy again — which is the only way the bug comes back.
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
		if strings.Contains(src, "const num =") || strings.Contains(src, "function num(") {
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
