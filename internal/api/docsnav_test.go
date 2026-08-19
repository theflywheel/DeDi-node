package api

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Every documentation page must carry the contents, not a link back to them.
//
// The index existed but rendered at line 128 of a 148-line page — a footer. A
// reader had to scroll past the entire explainer to discover the documentation
// existed, and a page they were already reading offered no way to reach a
// sibling. Nineteen documents with a deliberate reading order need navigation
// that is present rather than findable.
func TestEveryDocPageCarriesTheContents(t *testing.T) {
	srv, _, _ := testServer(t)
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	for slug := range docs {
		resp, err := http.Get(srv.URL + "/docs/" + slug)
		if err != nil {
			t.Fatalf("GET /docs/%s: %v", slug, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		page := string(body)
		if !strings.Contains(page, `class="toc"`) {
			t.Errorf("/docs/%s has no contents; a reader there can only go back", slug)
			continue
		}
		// The page must say where the reader is, or the contents is just a
		// list of links they have to read to locate themselves in.
		if !strings.Contains(page, `aria-current="page"`) {
			t.Errorf("/docs/%s does not mark itself in the contents", slug)
		}
	}
}

// The reading order in `sections` is deliberate; prev/next is what makes it
// usable rather than merely present.
func TestReadingOrderIsNavigable(t *testing.T) {
	srv, _, _ := testServer(t)
	order := readingOrder()
	if len(order) < 2 {
		t.Fatal("no reading order to navigate")
	}
	docs, _ := loadDocs()

	// The first page has a next and no previous; the last, the reverse.
	first := docs[order[0]]
	last := docs[order[len(order)-1]]
	if first == nil || last == nil {
		t.Fatal("the reading order names a document that is not embedded")
	}
	pn := regexp.MustCompile(`(?s)<div class="pn">(.*?)</div>`)

	get := func(slug string) string {
		resp, err := http.Get(srv.URL + "/docs/" + slug)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		m := pn.FindSubmatch(b)
		if m == nil {
			t.Fatalf("/docs/%s has no prev/next", slug)
		}
		return string(m[1])
	}

	if strings.Contains(get(order[0]), "←") {
		t.Error("the first page in the reading order offers a previous")
	}
	if !strings.Contains(get(order[0]), "→") {
		t.Error("the first page offers no next")
	}
	if strings.Contains(get(order[len(order)-1]), "→") {
		t.Error("the last page in the reading order offers a next")
	}

	// A middle page points at its actual neighbours, not at anything.
	mid := order[len(order)/2]
	seg := get(mid)
	wantPrev := "/docs/" + order[len(order)/2-1]
	wantNext := "/docs/" + order[len(order)/2+1]
	if !strings.Contains(seg, wantPrev) || !strings.Contains(seg, wantNext) {
		t.Errorf("/docs/%s does not link its neighbours %s and %s: %s", mid, wantPrev, wantNext, seg)
	}
}

// Every embedded document must appear in the reading order. One that is not is
// reachable only from the index, and never turns up while reading through.
func TestEveryDocIsInTheReadingOrder(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	inOrder := map[string]bool{}
	for _, slug := range readingOrder() {
		inOrder[slug] = true
	}
	for slug := range docs {
		if !inOrder[slug] {
			t.Errorf("%s is embedded but sits in no section, so nothing leads to it", slug)
		}
	}
}

// The contents must be near the top of /docs. It was at line 128 of 148, which
// is why the page read as having no index at all.
func TestTheDocsIndexIsNotAFooter(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	i := strings.Index(page, "All documentation")
	if i < 0 {
		t.Fatal("/docs no longer carries an index")
	}
	// Generously: within the first third of the page.
	if i > len(page)/3 {
		t.Errorf("the index starts %d%% of the way down /docs — a reader must scroll past the "+
			"explainer to learn the documentation exists", 100*i/len(page))
	}
}
