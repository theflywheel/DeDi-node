package api

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Nothing may make the page itself scroll sideways on a phone.
//
// Three causes, all found from one report:
//
//  1. The nav. Generating it removed the whitespace the hardcoded versions had
//     between links — `</a> <a` became `</a><a` — and inline elements with no
//     whitespace between them offer the browser no break opportunity. Eight
//     links became one unbreakable run. It is now a flex container that wraps,
//     which does not depend on source whitespace at all.
//  2. Table cells. A base64 root, a log origin or a verifier key is a long
//     unbroken token, and a cell that cannot break one is as wide as it is.
//  3. Wide tables. A markdown table has as many columns as its author wanted;
//     it scrolls in its own box, the way pre blocks already did.
func TestPagesDoNotForceHorizontalScroll(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")
	pages := []string{"/", "/browse", "/network", "/status", "/check", "/verify", "/docs", "/admin",
		"/docs/witnessing", "/docs/replication"}

	for _, p := range pages {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		page := string(body)

		// The nav must be able to wrap.
		if !strings.Contains(page, "flex-wrap: wrap") {
			t.Errorf("%s: the nav cannot wrap, so its links are one unbreakable run", p)
		}
		// Cells must be able to break a long token.
		if strings.Contains(page, "<table") && !strings.Contains(page, "overflow-wrap: anywhere") {
			t.Errorf("%s: table cells cannot break a long token, so a root hash sets the width", p)
		}
	}
}

// Whitespace between inline links is a break opportunity, and generating the
// nav silently removed it. Belt and braces alongside the flex rule: if someone
// later reverts to inline layout, the markup must still be breakable.
func TestGeneratedNavIsBreakable(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	i := strings.Index(page, "<nav>")
	j := strings.Index(page, "</nav>")
	if i < 0 || j < i {
		t.Fatal("no nav served")
	}
	nav := page[i:j]
	links := strings.Count(nav, "<a ")
	if links < 5 {
		t.Fatalf("only %d links in the nav; this test needs rewriting", links)
	}
	// Either the nav wraps by layout, or there is whitespace to break at.
	wrapsByLayout := strings.Contains(page, "flex-wrap: wrap")
	hasBreaks := strings.Contains(nav, "</a> <a") || strings.Contains(nav, "</a>\n")
	if !wrapsByLayout && !hasBreaks {
		t.Error("the nav neither wraps nor contains a break opportunity between its links")
	}
}

// A multi-column layout must actually resolve to one column on a phone.
//
// The browser page introduced the first two-column reading layout, a third way
// to push a phone sideways after the nav and the tables.
//
// Two earlier versions of this test were wrong in opposite directions, which is
// worth recording because both looked fine. The first asked only whether a
// "1fr" and an "@media" appeared somewhere in the file — true of a page whose
// unconditional rule is two columns. The second tried to honour media queries
// with a regex, and a regex cannot see a nested block: it read the rules INSIDE
// every @media as though they were top-level, and reported two pages that were
// already correct. So this scans braces properly, evaluates each query at a
// 375px viewport, and asks what each selector actually computes to there.
func TestEveryMultiColumnLayoutCollapsesOnAPhone(t *testing.T) {
	const phonePx = 375.0
	for name, page := range servedPages(t) {
		css := withoutComments(page)
		if i := strings.Index(css, "<style>"); i >= 0 {
			css = css[i+len("<style>"):]
		}
		for sel, cols := range effectiveGrids(css, phonePx) {
			// repeat(auto-fit, minmax(X, 1fr)) resolves to however many columns
			// fit, and so to one on a narrow container, whatever the written
			// track list looks like.
			if strings.Contains(cols, "auto-fit") || strings.Contains(cols, "auto-fill") {
				continue
			}
			if n := len(strings.Fields(cols)); n > 1 {
				t.Errorf("%s: at %.0fpx, %q still lays out %d columns (%q)", name, phonePx, sel, n, cols)
			}
		}
	}
}

var mediaRE = regexp.MustCompile(`@media[^{]*\((min|max)-width:\s*([0-9.]+)(px|em)\)`)

// effectiveGrids returns, per selector, the grid-template-columns that applies
// at the given viewport width — later rules winning, and rules inside a query
// that does not match at that width ignored.
func effectiveGrids(css string, viewportPx float64) map[string]string {
	out := map[string]string{}
	var scan func(s string, active bool)
	scan = func(s string, active bool) {
		for i := 0; i < len(s); {
			open := strings.IndexByte(s[i:], '{')
			if open < 0 {
				return
			}
			open += i
			close := matchingBrace(s, open)
			if close < 0 {
				return
			}
			sel := strings.TrimSpace(s[i:open])
			body := s[open+1 : close]
			if strings.HasPrefix(sel, "@media") {
				applies := false
				if m := mediaRE.FindStringSubmatch(sel); m != nil {
					px, _ := strconv.ParseFloat(m[2], 64)
					if m[3] == "em" {
						px *= 16 // the pages set no root font-size, so 1em is 16px
					}
					applies = (m[1] == "min" && viewportPx >= px) || (m[1] == "max" && viewportPx <= px)
				}
				scan(body, active && applies)
			} else if strings.HasPrefix(sel, "@") {
				scan(body, active) // @supports and friends: descend, do not filter
			} else if active {
				for _, d := range regexp.MustCompile(`grid-template-columns:\s*([^;}]+)`).FindAllStringSubmatch(body, -1) {
					for _, one := range strings.Split(sel, ",") {
						out[strings.TrimSpace(one)] = strings.TrimSpace(d[1])
					}
				}
			}
			i = close + 1
		}
	}
	scan(css, true)
	return out
}

// matchingBrace returns the index of the brace closing the one at open.
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
