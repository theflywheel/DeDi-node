package api

import (
	stdhtml "html"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Refiling a document between sections is an edit to one line of a slice, and
// dropping it from the sidebar entirely is the same edit with a slug missing.
// Nothing about the page would look wrong afterwards: the document still
// renders, still has prev/next, and simply stops appearing in the contents.
//
// Every doc that exists must be filed exactly once. The expectation comes from
// the filesystem rather than from the same slice under test.
func TestEveryDocIsFiledInExactlyOneSection(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) < 10 {
		t.Fatalf("only %d documents loaded; this test has stopped seeing them", len(docs))
	}
	seen := map[string]int{}
	for _, sec := range sections {
		for _, slug := range sec.Slugs {
			seen[slug]++
			if _, ok := docs[slug]; !ok {
				t.Errorf("section %q files %q, which is not a document", sec.Name, slug)
			}
		}
	}
	for slug := range docs {
		switch seen[slug] {
		case 1:
		case 0:
			t.Errorf("%s exists and is in no section, so the contents never lists it", slug)
		default:
			t.Errorf("%s is filed in %d sections", slug, seen[slug])
		}
	}
}

// The section label is the one part of the canvas's breadcrumb that says
// something the page does not already say. It has to actually appear, and it
// has to name the section this page is really in rather than a fixed string.
func TestADocPageNamesTheSectionItIsIn(t *testing.T) {
	srv, _, _ := testServer(t)
	// Two pages from different sections: a label hard-coded to one section
	// passes a single-page test.
	for slug, want := range map[string]string{
		"witnessing":  "Proving it",
		"conformance": "Against the standard",
		"delegation":  "Operating it",
		"why":         "Start here",
	} {
		body := mustGetBodyOf(t, srv.URL+"/docs/"+slug)
		i := strings.Index(body, `class="eyebrow"`)
		if i < 0 {
			t.Errorf("/docs/%s does not name its section at all", slug)
			continue
		}
		line := body[i:]
		if j := strings.Index(line, "</p>"); j > 0 {
			line = line[:j]
		}
		if !strings.Contains(line, want) {
			t.Errorf("/docs/%s is filed under %q; the page says %q", slug, want, line)
		}
		// The canvas drew a counter beside it. It is deliberately not here:
		// these are grouped documents, not a numbered course.
		if strings.Contains(line, " of ") {
			t.Errorf("/docs/%s prints a position counter: %q", slug, line)
		}
	}
}

// The label must sit above the document's own title, inside the article -- an
// eyebrow that follows the h1 is not an eyebrow, it is a stray line of small
// caps, and one rendered outside <main> is in the contents column.
//
// Byte offsets in the whole page cannot tell either of those apart: moving the
// label ahead of the sidebar keeps it earlier in the document than the h1 while
// putting it in the wrong column entirely. This asks the parsed tree instead.
func TestTheSectionLabelSitsAboveTheTitleInsideTheArticle(t *testing.T) {
	srv, _, _ := testServer(t)
	doc := parsePage(t, srv.URL+"/docs/witnessing")

	var eyebrow, h1 *html.Node
	var order []string
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if hasClass(n, "eyebrow") && eyebrow == nil {
			eyebrow = n
			order = append(order, "eyebrow")
		}
		if n.Data == "h1" && h1 == nil {
			h1 = n
			order = append(order, "h1")
		}
	})
	if eyebrow == nil {
		t.Fatal("the page does not name its section")
	}
	if h1 == nil {
		t.Fatal("the page has no title")
	}
	if len(order) < 2 || order[0] != "eyebrow" {
		t.Errorf("the section label renders after the page title: %v", order)
	}
	// walk is document order, so the eyebrow coming first is necessary but not
	// sufficient: it must also be in the article rather than the contents.
	if !ancestorWith(eyebrow, "toc") {
		// good -- but say what we actually require, positively:
		inMain := false
		for p := eyebrow.Parent; p != nil; p = p.Parent {
			if p.Type == html.ElementNode && p.Data == "main" {
				inMain = true
				break
			}
		}
		if !inMain {
			t.Error("the section label is not inside the article")
		}
	} else {
		t.Error("the section label renders inside the contents column")
	}
}

// docEyebrow is pure, and its empty case is the one a page never shows: the
// not-found page has no slug and no section. Without this, deleting the guard
// ships a bare <p class="eyebrow"></p> and nothing fails.
func TestAPageInNoSectionIsNotGivenAnEmptyLabel(t *testing.T) {
	for _, slug := range []string{"", "no-such-slug", "docs", "index"} {
		if got := docEyebrow(slug); got != "" {
			t.Errorf("docEyebrow(%q) = %q; a page in no section has no section to name", slug, got)
		}
	}
	// ...and the guard must not be so broad that it swallows real pages.
	if docEyebrow("witnessing") == "" {
		t.Error("docEyebrow(\"witnessing\") is empty; the guard has eaten a filed page")
	}
}

// The ordering this change makes is one line of a slice, and every renderer
// derives from that same line -- so index/sidebar agreement cannot see it move.
// These are the two orderings the change is actually for, stated literally.
func TestConformanceIsReadBesideTheGapsItPairsWith(t *testing.T) {
	order := readingOrder()
	pos := map[string]int{}
	for i, slug := range order {
		pos[slug] = i
	}
	for _, slug := range []string{"conformance", "spec-gaps"} {
		if _, ok := pos[slug]; !ok {
			t.Fatalf("%s is not in the reading order at all", slug)
		}
	}
	if pos["conformance"]+1 != pos["spec-gaps"] {
		t.Errorf("conformance is at %d and spec-gaps at %d; they answer the same question "+
			"and are meant to be read one after the other", pos["conformance"], pos["spec-gaps"])
	}
	if got := docSectionOf("conformance"); got != "Against the standard" {
		t.Errorf("conformance is filed under %q, not with the specification material", got)
	}
	// It leads that section: what matches, before what does not.
	for _, sec := range sections {
		if sec.Name == "Against the standard" {
			if len(sec.Slugs) == 0 || sec.Slugs[0] != "conformance" {
				t.Errorf("%q opens with %v, not with what matches", sec.Name, sec.Slugs)
			}
		}
	}
}

// The index and the sidebar are two renderings of one ordering. They disagreed
// silently once already, and a reader who meets the docs through the index and
// then navigates by the sidebar sees two different tables of contents.
func TestTheIndexAndTheSidebarAgreeOnTheOrder(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	idx := s.docIndex()
	side := docSidebar("", docs)
	var last int
	for _, slug := range readingOrder() {
		d, ok := docs[slug]
		if !ok {
			continue
		}
		i := strings.Index(idx, "/docs/"+slug+`"`)
		if i < 0 {
			t.Errorf("%s is in the reading order and not on the index page", slug)
			continue
		}
		if i < last {
			t.Errorf("%s appears on the index out of reading order", slug)
		}
		last = i
		if !strings.Contains(side, "/docs/"+slug+`"`) {
			t.Errorf("%s is in the reading order and not in the sidebar", slug)
		}
		if !strings.Contains(side, stdhtml.EscapeString(d.Title)) {
			t.Errorf("%s is in the sidebar under something other than its own title", slug)
		}
	}
}
