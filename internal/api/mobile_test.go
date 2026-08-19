package api

import (
	"io"
	"net/http"
	"regexp"
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

// A multi-column layout must have a single-column path for a narrow screen.
//
// The browser page introduced the first two-column reading layout, which is a
// third way to push a phone sideways after the nav and the tables. This does
// not mandate which direction the media query runs — the pages that already
// had columns collapse with max-width, the browser expands with min-width, and
// both give a phone one column — only that a page which can produce two
// columns can also produce one.
func TestEveryMultiColumnLayoutCollapsesOnAPhone(t *testing.T) {
	for name, page := range servedPages(t) {
		css := withoutComments(page)
		multi := false
		for _, decl := range regexp.MustCompile(`grid-template-columns:\s*([^;}]+)`).FindAllStringSubmatch(css, -1) {
			// repeat(auto-fit, minmax(X, 1fr)) already collapses to one column
			// once the container is narrower than X, with no query involved.
			// Flagging it cost this test its first run on a page that was
			// already correct.
			if strings.Contains(decl[1], "auto-fit") || strings.Contains(decl[1], "auto-fill") {
				continue
			}
			if len(strings.Fields(decl[1])) > 1 {
				multi = true
			}
		}
		if !multi {
			continue
		}
		single := regexp.MustCompile(`grid-template-columns:\s*1fr\s*[;}]`).MatchString(css)
		if !single || !strings.Contains(css, "@media") {
			t.Errorf("%s: lays out in multiple columns with no single-column rule for a narrow screen", name)
		}
	}
}
