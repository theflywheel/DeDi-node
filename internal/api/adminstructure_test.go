package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func adminPageBody(t *testing.T) string {
	t.Helper()
	srv, _, _ := writeServer(t, "flywheel")
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin: %d", resp.StatusCode)
	}
	return string(b)
}

// Parse the console rather than search it.
//
// This page is 1300 lines and the restructure moves whole sections between
// parents. A stray or missing </div> would reparent everything after it — the
// exact failure the documentation sidebar had, where four tests substring-
// matched `class="toc"`, all passed, and all nineteen pages were broken.
// Counting <div> against </div> with a regex would not have caught that either,
// because the counts balanced; what was wrong was where they nested.
//
// So: parse the real served page, walk the tree, and ask about structure.
func adminTree(t *testing.T) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(adminPageBody(t)))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func byID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := byID(c, id); got != nil {
			return got
		}
	}
	return nil
}

func hasAncestorID(n *html.Node, id string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		for _, a := range p.Attr {
			if a.Key == "id" && a.Val == id {
				return true
			}
		}
	}
	return false
}

// Every tab's section must be a section: a real element, and a sibling of the
// others rather than nested inside one. A view nested inside another view is
// hidden whenever its host is, which reads as a tab that does nothing.
func TestEachConsoleSectionIsItsOwnTopLevelBlock(t *testing.T) {
	doc := adminTree(t)
	views := []string{"view-list", "view-children", "view-addnode", "view-push", "view-domain"}
	for _, v := range views {
		n := byID(doc, v)
		if n == nil {
			t.Errorf("%s is not in the page at all", v)
			continue
		}
		for _, other := range views {
			if other != v && hasAncestorID(n, other) {
				t.Errorf("%s is nested inside %s, so it is hidden whenever %s is", v, other, other)
			}
		}
	}
}

// The tab strip and the sections it switches between have to agree. A tab with
// no section shows nothing; a section with no tab is unreachable.
func TestEveryConsoleTabHasASectionAndViceVersa(t *testing.T) {
	doc := adminTree(t)
	page := adminPageBody(t)

	// The VIEWS list the script switches on.
	i := strings.Index(page, "const VIEWS = [")
	if i < 0 {
		t.Fatal("no VIEWS list in the console")
	}
	list := page[i+len("const VIEWS = [") : i+strings.Index(page[i:], "]")]
	var views []string
	for _, part := range strings.Split(list, ",") {
		if v := strings.Trim(strings.TrimSpace(part), "'\""); v != "" {
			views = append(views, v)
		}
	}
	if len(views) == 0 {
		t.Fatal("parsed no views, so this proves nothing")
	}
	for _, v := range views {
		if byID(doc, "view-"+v) == nil {
			t.Errorf("tab %q switches to #view-%s, which does not exist — the tab shows nothing", v, v)
		}
		if byID(doc, "tab-"+v) == nil {
			t.Errorf("#view-%s has no #tab-%s, so nothing can reach it", v, v)
		}
	}
}

// The two-column layout must not put the onboarding form inside the table's
// own scroll box, and the participants table must still be inside the section
// its tab reveals.
func TestParticipantsAndOnboardAreSiblingColumns(t *testing.T) {
	doc := adminTree(t)
	parts, onboard := byID(doc, "participants"), byID(doc, "onboard-col")
	if parts == nil || onboard == nil {
		t.Fatal("the participants table or the onboarding column is missing")
	}
	if !hasAncestorID(parts, "view-list") || !hasAncestorID(onboard, "view-list") {
		t.Error("a column escaped the participants section, so its tab no longer controls it")
	}
	if hasAncestorID(onboard, "participants") {
		t.Error("the onboarding form is inside the participants table's own box")
	}
}
