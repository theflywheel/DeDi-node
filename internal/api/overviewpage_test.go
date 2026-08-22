package api

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func frontPage(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// A 404 body satisfies a "does not contain" assertion perfectly. Every test
	// below asks what the front door says, which presumes it answered.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: %v", url, err)
	}
	return string(b)
}

// A read-only node must never advertise a write plane it does not have.
//
// Most nodes running this binary are read-only, so that is the case the test
// leads with: a fixture that always configures a publisher key would assert the
// rare state and pass while the common one was wrong.
func TestFrontDoorSaysWhetherThisNodeCanBeWrittenTo(t *testing.T) {
	// No publisher keys: the write routes are not even registered.
	ro, _, _ := testServer(t)
	page := frontPage(t, ro.URL+"/")
	if !strings.Contains(page, "closed") {
		t.Error("a node with no publisher keys does not say its write plane is closed")
	}
	if regexp.MustCompile(`write plane</td><td>[^<]*open`).MatchString(page) {
		t.Error("a read-only node advertises an open write plane")
	}

	// With keys, it says so — and still does not say how many.
	rw, _, _ := writeServer(t, "flywheel")
	page = frontPage(t, rw.URL+"/")
	if !regexp.MustCompile(`write plane</td><td>[^<]*open`).MatchString(page) {
		t.Error("a node with a write plane does not say so")
	}
	// The count of publisher keys is reconnaissance on an unauthenticated page,
	// and works against the reason the write routes are unregistered at all.
	if regexp.MustCompile(`(?i)\d+\s+publisher key`).MatchString(page) {
		t.Error("the front door publishes how many publisher keys are loaded")
	}
}

// The documentation count must track the documentation.
//
// The design writes "19 pages" as sample data. A literal would be wrong the
// first time anyone added one, so this compares what the page says against what
// the binary actually carries.
func TestFrontDoorCountsTheDocsItActuallyHas(t *testing.T) {
	srv, _, _ := testServer(t)
	page := frontPage(t, srv.URL+"/")
	docs, err := loadDocs()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("this build carries no docs, so this proves nothing")
	}
	want := strconv.Itoa(len(docs)) + " pages"
	if !strings.Contains(page, want) {
		t.Errorf("the front page does not say %q; it carries %d documentation pages", want, len(docs))
	}
}

// Every placeholder must actually be substituted.
//
// A renamed or forgotten one reaches the reader as literal template text, and
// the page keeps rendering as though nothing were wrong — the same silent
// degradation as a token nothing defines or a metric the node never emits.
func TestNoServedPageLeaksATemplatePlaceholder(t *testing.T) {
	// Real requests, not servedPages(): that helper calls writePage directly,
	// which substitutes only the placeholders EVERY page shares. The rest —
	// the verifier key, the doc index, the role catalogue, the write plane —
	// are filled by the individual handlers, so checking writePage's output
	// reported every one of them as leaked while the served pages were fine.
	// A test looking at the wrong layer accuses the right code.
	srv, _, _ := writeServer(t, "flywheel")
	leftover := regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
	for _, path := range []string{"/", "/browse", "/network", "/status", "/check", "/verify",
		"/docs", "/docs/witnessing", "/admin"} {
		page := frontPage(t, srv.URL+path)
		if m := leftover.FindString(page); m != "" {
			t.Errorf("%s is served with %s still in it", path, m)
		}
	}
}

// An element nothing writes to is a promise the page does not keep.
//
// The browse card carried an id for live counts that no script ever populated,
// because the node cannot produce those counts at all — every store query takes
// a namespace and nothing lists them. The empty subtitle looked like a loading
// state that never resolved.
func TestNoPagePromisesAnElementNothingFills(t *testing.T) {
	ids := regexp.MustCompile(`id="([a-z][a-z0-9-]*)"`)
	for name, raw := range map[string][]byte{
		"overview.html": overviewHTML, "status.html": statusPageHTML,
		"network.html": networkPageHTML, "check.html": checkPageHTML,
		"index.html": explorerHTML,
	} {
		src := withoutComments(string(raw))
		script := src[strings.Index(src, "<script"):]
		for _, m := range ids.FindAllStringSubmatch(src, -1) {
			id := m[1]
			// Referenced by the page's own script, or by a label/aria pointing
			// at a form control the user fills.
			if strings.Contains(script, "'"+id+"'") || strings.Contains(script, `"`+id+`"`) ||
				strings.Contains(src, `for="`+id+`"`) || strings.Contains(src, `aria-labelledby="`+id+`"`) {
				continue
			}
			t.Errorf("%s: #%s is never written to or referenced — a name the page promises and "+
				"nothing keeps", name, id)
		}
	}
}
