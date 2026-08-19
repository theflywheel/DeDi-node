package api

import (
	"regexp"
	"strings"
	"testing"
)

// One palette, one type scale, across every surface.
//
// Two mismatches were reported from a phone and both were real:
//
//   - /verify rendered about a quarter larger than everything else. Bare
//     `font-family: monospace` triggers the browser's monospace font-size
//     quirk — the page gets the "default fixed" size, 13px in Chrome and
//     Safari, rather than 16px. /verify named a real family first and so
//     escaped it. Every surface now names one.
//   - The same meanings had four different colours. "This verified" was
//     `green` on five pages and #1a7f37 on /verify; muted was #666 or #6b6b6b.
//     Drift a reader feels without being able to name.
//
// Matches monospace as the ONLY family — which is what triggers the quirk. A
// proper stack ends with the generic keyword, so matching it anywhere flagged
// every correct page, including the ones this test exists to protect.
var bareMono = regexp.MustCompile(`font-family:\s*monospace\s*[;}]`)

func TestEverySurfaceSharesTheTypeAndPalette(t *testing.T) {
	// Read what is SERVED, not what is on disk. The palette now arrives by
	// injection from static/components.css, so a page whose source no longer
	// spells out --ok is correct — and a page that never received the sheet is
	// the actual bug this test is looking for. Checking the source files would
	// have inverted both answers.
	for name, page := range servedPages(t) {
		if bareMono.MatchString(page) {
			t.Errorf("%s uses the bare monospace keyword, so it renders at 13px while the "+
				"pages that name a family render at 16px", name)
		}
		if !strings.Contains(page, "ui-monospace") {
			t.Errorf("%s does not use the shared font stack", name)
		}
		if !strings.Contains(page, "--ok:") {
			t.Errorf("%s does not define the shared semantic palette", name)
		}
		// A token defined as itself is cyclic: it computes invalid and every
		// use silently falls back to the inherited value. A blanket
		// find-and-replace of the literal colour did exactly this to five
		// pages, and nothing looked wrong in the source.
		for _, tok := range []string{"ok", "bad", "mut", "wit", "warn"} {
			if strings.Contains(page, "--"+tok+": var(--"+tok+")") {
				t.Errorf("%s defines --%s as itself, so every use of it computes invalid", name, tok)
			}
		}
		// The old literals must not creep back in beside the tokens.
		for _, stale := range []string{"color: green;", "color: #b00;", "color: #666;"} {
			if strings.Contains(page, stale) {
				t.Errorf("%s still hardcodes %q instead of the shared token", name, stale)
			}
		}
	}
}

// The generated documentation pages are a surface too, and were the easiest to
// forget: their CSS lives in Go rather than in a .html file.
func TestGeneratedDocPagesShareTheTypeAndPalette(t *testing.T) {
	page := (&Server{}).docLayout("t", "<p>b</p>")
	if bareMono.MatchString(page) {
		t.Error("generated doc pages use the bare monospace keyword")
	}
	if !strings.Contains(page, "--ok:") {
		t.Error("generated doc pages do not define the shared palette")
	}
}
