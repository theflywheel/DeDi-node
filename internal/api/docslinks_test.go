package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Every link a reader can click on a served doc page, and on the /docs landing
// page, must land somewhere real: fetched through the node, not reasoned about
// from the markdown. The docs were written to be read in a repository browser,
// so `foo.md` links worked there and 404'd on every node — about thirty of
// them, on the only surface most readers reach. Fragments are checked too: a
// link to /docs/x#part where x has no such heading scrolls nowhere.
func TestEveryDocLinkResolves(t *testing.T) {
	srv, _, _ := testServer(t)
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(srv.URL)
	pages := []string{"/docs"}
	for slug := range docs {
		pages = append(pages, "/docs/"+slug)
	}

	ids := map[string]map[string]bool{} // element ids by path, for fragments
	idsOf := func(path string) map[string]bool {
		if m, ok := ids[path]; ok {
			return m
		}
		m := map[string]bool{}
		walk(parsePage(t, srv.URL+path), func(n *html.Node) {
			if n.Type == html.ElementNode {
				if id := attr(n, "id"); id != "" {
					m[id] = true
				}
			}
		})
		ids[path] = m
		return m
	}
	status := map[string]int{}

	for _, page := range pages {
		from, _ := url.Parse(srv.URL + page)
		walk(parsePage(t, srv.URL+page), func(n *html.Node) {
			if n.Type != html.ElementNode || n.Data != "a" {
				return
			}
			href := attr(n, "href")
			if href == "" {
				return
			}
			to, err := from.Parse(href)
			if err != nil {
				t.Errorf("%s: unparseable link %q", page, href)
				return
			}
			// The source repository is private: a reader of a node's docs
			// following this link gets a 404 from GitHub.
			if strings.Contains(href, "github.com/theflywheel/DeDi-node") {
				t.Errorf("%s links to %q, a private repository readers cannot open", page, href)
			}
			if to.Scheme != "http" || to.Host != base.Host {
				return // external, or mailto: — not this node's to answer for
			}
			if strings.HasSuffix(to.Path, ".md") {
				t.Errorf("%s links to %q, a markdown path the node does not serve", page, href)
			}
			code, seen := status[to.Path]
			if !seen {
				resp, err := http.Get(srv.URL + to.Path)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				code = resp.StatusCode
				status[to.Path] = code
			}
			if code != http.StatusOK {
				t.Errorf("%s links to %q, which answers %d", page, href, code)
				return
			}
			if to.Fragment != "" && strings.HasPrefix(to.Path, "/docs") && !idsOf(to.Path)[to.Fragment] {
				t.Errorf("%s links to %q, and %s has no element with that id", page, href, to.Path)
			}
		})
	}
}
