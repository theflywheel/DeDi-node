package api

import (
	"regexp"
	"strings"
	"testing"
)

// The documentation is embedded at build time, so everything here is decidable
// without a server or a database: these tests read what the binary would serve.

func TestEveryDocRenders(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no documentation embedded — /docs would serve an empty index")
	}
	for slug, d := range docs {
		if strings.TrimSpace(d.Body) == "" {
			t.Errorf("%s: rendered to nothing", slug)
		}
		if d.Title == slug {
			t.Errorf("%s: no H1, so the index would list it by filename", slug)
		}
		if d.Summary == "" {
			t.Errorf("%s: no prose paragraph, so the index would list it with no description", slug)
		}
	}
}

// A slug named in the index but absent from docs/ is silently skipped when the
// page renders, so the section simply loses a row and nobody finds out. This is
// the test that turns a rename into a build failure instead of a quiet gap.
func TestEverySectionSlugExists(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	for _, sec := range sections {
		for _, slug := range sec.Slugs {
			if _, ok := docs[slug]; !ok {
				t.Errorf("section %q lists %q, which is not in docs/", sec.Name, slug)
			}
		}
	}
}

// The converse: a document that exists but is in no section still has to be
// reachable, because a page nobody links to is findable only by someone who
// already knew it was there.
func TestEveryDocIsReachableFromTheIndex(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	index := (&Server{}).docIndex()
	for slug := range docs {
		if !strings.Contains(index, `href="/docs/`+slug+`"`) {
			t.Errorf("%s is embedded but the index does not link it", slug)
		}
	}
}

var docLink = regexp.MustCompile(`href="/docs/([a-z0-9-]+)"`)

// Cross-references between documents are the first thing to rot: a file gets
// renamed and every link to it becomes a 404 that only a reader discovers.
func TestInternalDocLinksResolve(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	for slug, d := range docs {
		for _, m := range docLink.FindAllStringSubmatch(d.Body, -1) {
			if _, ok := docs[m[1]]; !ok {
				t.Errorf("%s links to /docs/%s, which does not exist", slug, m[1])
			}
		}
	}
}

// Raw HTML is escaped rather than rendered (the renderer is built without
// WithUnsafe). Asserted rather than assumed, because "we did not pass that
// option" is the kind of fact that survives until someone adds the option to
// make one diagram work.
func TestRawHTMLInMarkdownIsNotRendered(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	for slug, d := range docs {
		if strings.Contains(d.Body, "<script") {
			t.Errorf("%s: rendered output contains a <script> tag", slug)
		}
	}
}

// Mermaid blocks have to reach the browser as <pre class="mermaid">, which is
// what the explainer page's script looks for. Rendered as a plain code fence
// they display as source, which looks like a broken diagram rather than a
// missing conversion.
func TestMermaidFencesBecomeDiagrams(t *testing.T) {
	docs, err := loadDocs()
	if err != nil {
		t.Fatalf("loadDocs: %v", err)
	}
	var found bool
	for slug, d := range docs {
		if strings.Contains(d.Body, `language-mermaid`) {
			t.Errorf("%s: a mermaid fence was left as a code block", slug)
		}
		if strings.Contains(d.Body, `<pre class="mermaid">`) {
			found = true
		}
	}
	if !found {
		t.Skip("no document currently contains a mermaid diagram")
	}
}
