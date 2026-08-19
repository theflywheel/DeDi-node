package api

import (
	"math"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The shared vocabulary has to reach every page, and reach it exactly once.
//
// The palette line had been copy-pasted into nine separate <style> blocks and
// had already drifted — the generated documentation pages were missing --warn,
// so a warning rendered there as unstyled inherited ink. These tests are about
// the two ways that recurs: a page that never receives the sheet, and a page
// that receives it and then quietly redefines part of it locally.

// servedPages renders every page through the real write path, so a placeholder
// nobody substituted shows up as the served bytes rather than being masked by
// reading the embedded source file. That distinction is the whole point: the
// last regression of this shape passed a test that string-matched the source.
func servedPages(t *testing.T) map[string]string {
	t.Helper()
	s := &Server{}
	out := map[string]string{}
	for name, raw := range map[string][]byte{
		"/":        overviewHTML,
		"/browse":  explorerHTML,
		"/network": networkPageHTML,
		"/status":  statusPageHTML,
		"/check":   checkPageHTML,
		"/verify":  verifyHTML,
		"/docs":    docsHTML,
		"/admin":   adminHTML,
	} {
		w := httptest.NewRecorder()
		s.writePage(w, raw, name)
		out[name] = w.Body.String()
	}
	// A documentation page builds its own shell in Go and never passes through
	// writePage, which is exactly why it was the one that had drifted.
	out["/docs/{page}"] = s.docLayout("t", "<p>b</p>")
	return out
}

// withoutComments removes CSS and HTML comments, so an assertion is about what
// the browser will act on and not about prose that happens to name a token.
// The first run of these tests failed on a comment explaining a token choice.
var commentRE = regexp.MustCompile(`(?s)/\*.*?\*/|<!--.*?-->`)

func withoutComments(s string) string { return commentRE.ReplaceAllString(s, " ") }

func TestEveryServedPageCarriesTheSharedVocabulary(t *testing.T) {
	for name, raw := range servedPages(t) {
		page := withoutComments(raw)
		if strings.Contains(page, "{{SHARED_CSS}}") {
			t.Errorf("%s: served with the placeholder still in it, so it has no palette at all", name)
			continue
		}
		// The outcome each page needs: the five meaning-carrying colours are
		// defined, and the pill variants that spend them have rules.
		// Each token must resolve to an actual colour. Asserting only that the
		// name appears would accept `--ok:;` or a token defined as itself,
		// which computes invalid and silently falls back to inherited ink —
		// the exact bug a blanket find-and-replace caused here once before.
		for _, tok := range []string{"ok", "bad", "mut", "wit", "warn"} {
			re := regexp.MustCompile(`--` + tok + `:\s*#[0-9a-fA-F]{6}`)
			if !re.MatchString(page) {
				t.Errorf("%s: --%s does not resolve to a colour; the sheet did not reach this page, or the token is empty", name, tok)
			}
		}
		// And each variant must actually declare its colour. `.pill-ok {}`
		// emptied to nothing still contains the substring ".pill-ok", so
		// matching the name alone proves the rule exists, not that it does
		// anything.
		for _, v := range []struct{ cls, tok string }{{"pill-ok", "ok"}, {"pill-bad", "bad"}, {"pill-wit", "wit"}} {
			re := regexp.MustCompile(`\.` + v.cls + `\s*\{[^}]*border-color:\s*var\(--` + v.tok + `\)`)
			if !re.MatchString(page) {
				t.Errorf("%s: .%s does not set its border-color from --%s, so the state it marks is invisible", name, v.cls, v.tok)
			}
		}
	}
}

// A variant a page uses but nothing defines renders as an unstyled span: the
// state is claimed in the markup and invisible on screen.
func TestNoPageUsesAPillVariantItCannotStyle(t *testing.T) {
	used := regexp.MustCompile(`class="[^"]*\b(pill-[a-z]+|btn-[a-z]+)\b`)
	for name, raw := range servedPages(t) {
		page := withoutComments(raw)
		for _, m := range used.FindAllStringSubmatch(page, -1) {
			if !strings.Contains(page, "."+m[1]+" ") && !strings.Contains(page, "."+m[1]+"{") &&
				!strings.Contains(page, "."+m[1]+",") {
				t.Errorf("%s: uses .%s but no rule in the served page defines it", name, m[1])
			}
		}
	}
}

// The anti-drift assertion. The sheet only stays the single source of truth for
// as long as no page defines its own competing copy, which is how the nav ended
// up as four different navs.
func TestNoPageRedefinesTheSharedTokensOrComponents(t *testing.T) {
	// `.pill` without the hyphen has to be in here too. admin.html defined its
	// own bare .pill, and because `border:` is a shorthand it reset the
	// border-color a variant had just set — so `class="pill pill-ok"` would have
	// rendered grey on that page alone. A guard that only sees .pill- is the
	// "added to one implementation and not the other" bug in miniature.
	local := regexp.MustCompile(`(--ok|--bad|--wit|--warn):|\.pill[ ,{]|\.pill-|\.btn-primary`)
	for name, raw := range map[string][]byte{
		"overview.html": overviewHTML, "index.html": explorerHTML,
		"network.html": networkPageHTML, "status.html": statusPageHTML,
		"check.html": checkPageHTML, "verify.html": verifyHTML,
		"docs.html": docsHTML, "admin.html": adminHTML,
	} {
		if m := local.FindString(withoutComments(string(raw))); m != "" {
			t.Errorf("%s: defines %q itself; it belongs in static/components.css so every page agrees", name, m)
		}
	}
}

// relLum is the WCAG relative luminance of a #rrggbb colour.
func relLum(hex string) float64 {
	c := func(i int) float64 {
		v, _ := strconv.ParseInt(hex[i:i+2], 16, 32)
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*c(1) + 0.7152*c(3) + 0.0722*c(5)
}

func contrast(a, b string) float64 {
	x, y := relLum(a), relLum(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}

// The bug the design canvas actually names: a browser's default button border
// is invisible against the #f4f4f4 used for banners and table headers, so the
// control is not merely plain, it is absent.
//
// This asserts the property rather than the declaration — it reads whatever
// border colour the sheet ships and measures it — so changing the hex to
// another invisible one fails, and changing it to another visible one passes.
func TestButtonBordersAreVisibleAgainstTheBannerGrey(t *testing.T) {
	const banner = "#f4f4f4"
	css := withoutComments(string(componentsCSS))
	rule := regexp.MustCompile(`(?s)button, \.btn \{(.*?)\}`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("no button rule in components.css")
	}
	border := regexp.MustCompile(`border: 1px solid (#[0-9a-fA-F]{6})`).FindStringSubmatch(rule[1])
	if border == nil {
		t.Fatal("the button rule declares no explicit border colour, which is the bug it exists to fix")
	}
	if got := contrast(border[1], banner); got < 3 {
		t.Errorf("button border %s on %s is %.2f:1; below 3:1 the control is invisible where it is most used", border[1], banner, got)
	}
}

// The page list in servedPages is hand-written, and a hand-written list of
// surfaces is how the generated documentation pages came to be missing --warn:
// somebody added a surface and updated eight places out of nine.
//
// This walks the embedded directory instead, so a new page cannot be added
// without either carrying the placeholder or failing here. It also pins the
// count-1 bytes.Replace in writePage: a page carrying the placeholder twice
// would have its second copy served literally to the reader.
func TestEveryEmbeddedPageCarriesExactlyOnePlaceholder(t *testing.T) {
	entries, err := staticPages.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		seen++
		b, err := staticPages.ReadFile("static/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		switch n := strings.Count(string(b), "{{SHARED_CSS}}"); n {
		case 1:
		case 0:
			t.Errorf("%s: no {{SHARED_CSS}}, so it is served with no palette", e.Name())
		default:
			t.Errorf("%s: %d copies of {{SHARED_CSS}}; writePage substitutes one, so the rest reach the reader as literal text", e.Name(), n)
		}
	}
	if seen == 0 {
		t.Fatal("walked no pages at all, so this test proves nothing")
	}
}
