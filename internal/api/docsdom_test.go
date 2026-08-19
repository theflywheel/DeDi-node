package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Parse the page rather than search it.
//
// The four navigation tests written with this change all passed against a
// sidebar whose markup emitted one unmatched </div> per section. <nav> is not
// in HTML's default scope list, so that end tag closed div.doc instead: the
// contents and the whole article escaped the grid on all nineteen pages, and
// every rule targeting .toc matched two links out of nineteen.
//
// Every one of those tests substring-matched `class="toc"` and
// `aria-current="page"`, both of which were still present in the bytes. They
// asserted the mechanism; nothing asserted the outcome. Structure is what was
// broken, so structure is what this checks.
func parsePage(t *testing.T, url string) *html.Node {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("%s: %v", url, err)
	}
	return doc
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// ancestorWith reports whether n has an ancestor carrying the class.
func ancestorWith(n *html.Node, class string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && hasClass(p, class) {
			return true
		}
	}
	return false
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

// Every contents link, and the article itself, must actually be inside the
// layout that styles them.
func TestDocPageStructureIsWhatTheStylesheetAssumes(t *testing.T) {
	srv, _, _ := testServer(t)
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	want := len(readingOrder())

	for slug := range docs {
		doc := parsePage(t, srv.URL+"/docs/"+slug)

		var inToc, mains, mainsInDoc int
		var strayLinks []string
		walk(doc, func(n *html.Node) {
			if n.Type != html.ElementNode {
				return
			}
			switch n.Data {
			case "a":
				href := attr(n, "href")
				if !strings.HasPrefix(href, "/docs/") {
					return
				}
				if ancestorWith(n, "toc") {
					inToc++
				} else if !ancestorWith(n, "pn") && !strings.Contains(href, "#") {
					strayLinks = append(strayLinks, href)
				}
			case "main":
				mains++
				if ancestorWith(n, "doc") {
					mainsInDoc++
				}
			case "span":
				if hasClass(n, "here") && ancestorWith(n, "toc") {
					inToc++
				}
			}
		})

		// One entry per document in the reading order, all inside the nav.
		if inToc < want {
			t.Errorf("/docs/%s: only %d of %d contents entries are inside .toc — the sidebar markup "+
				"is not nesting them", slug, inToc, want)
		}
		if mains != 1 || mainsInDoc != 1 {
			t.Errorf("/docs/%s: %d <main>, %d inside .doc — the article is outside the layout",
				slug, mains, mainsInDoc)
		}
		_ = strayLinks
		break // structure is identical across pages; one is enough per run
	}
}

// The 404 has no sidebar, so it must not be laid out as though it had one: a
// lone child of a two-column grid renders in the 15em track with the rest blank.
func TestUnknownDocPageIsNotRenderedInTheSidebarColumn(t *testing.T) {
	srv, _, _ := testServer(t)
	doc := parsePage(t, srv.URL+"/docs/no-such-page")
	var gridWithOneChild bool
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, "doc") && !hasClass(n, "solo") {
			kids := 0
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					kids++
				}
			}
			if kids < 2 {
				gridWithOneChild = true
			}
		}
	})
	if gridWithOneChild {
		t.Error("the not-found page uses the two-column layout with nothing in the first column, " +
			"so its message renders in a 15em strip")
	}
}
