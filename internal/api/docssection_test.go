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

// The label must sit above the document's own title, inside the article, and
// there must be exactly one of it.
//
// Byte offsets in the whole page could not tell "before the title" apart from
// "in the contents column", so this reads the parsed tree. Taking merely the
// first .eyebrow and the first h1 is not enough either: an <h1 class="eyebrow">
// is a single node that satisfies both, and a stray second label lower in the
// article satisfies neither test while being obviously wrong. So: exactly one,
// not a heading, and a preceding sibling of the title under the same parent.
//
// Run over every filed page, not one -- a layout that only breaks on the pages
// with long titles is still broken.
func TestTheSectionLabelSitsAboveTheTitleInsideTheArticle(t *testing.T) {
	srv, _, _ := testServer(t)
	for _, slug := range readingOrder() {
		doc := parsePage(t, srv.URL+"/docs/"+slug)

		var labels []*html.Node
		var h1 *html.Node
		walk(doc, func(n *html.Node) {
			if n.Type != html.ElementNode {
				return
			}
			if hasClass(n, "eyebrow") {
				labels = append(labels, n)
			}
			if n.Data == "h1" && h1 == nil {
				h1 = n
			}
		})
		if len(labels) != 1 {
			t.Errorf("/docs/%s carries %d section labels, want exactly 1", slug, len(labels))
			continue
		}
		label := labels[0]
		if h1 == nil {
			t.Errorf("/docs/%s has no title", slug)
			continue
		}
		if label == h1 {
			t.Errorf("/docs/%s puts the label ON the title; there is no separate label", slug)
			continue
		}
		switch label.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			t.Errorf("/docs/%s renders the label as <%s>, which puts it in the document outline",
				slug, label.Data)
		}
		if label.Parent != h1.Parent {
			t.Errorf("/docs/%s puts the label and the title in different containers", slug)
			continue
		}
		// Preceding sibling: walk forward from the label and expect to meet the
		// title. "Above" is a relationship, not a byte offset.
		before := false
		for n := label.NextSibling; n != nil; n = n.NextSibling {
			if n == h1 {
				before = true
				break
			}
		}
		if !before {
			t.Errorf("/docs/%s renders the section label after the page title", slug)
		}
		inMain := false
		for p := label.Parent; p != nil; p = p.Parent {
			if p.Type == html.ElementNode && p.Data == "main" {
				inMain = true
			}
			if p.Type == html.ElementNode && hasClass(p, "toc") {
				t.Errorf("/docs/%s renders the section label inside the contents column", slug)
			}
		}
		if !inMain {
			t.Errorf("/docs/%s renders the section label outside the article", slug)
		}
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
