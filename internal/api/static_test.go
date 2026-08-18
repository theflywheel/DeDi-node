package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestOverviewServedAtRoot(t *testing.T) {
	srv, _, _ := testServer(t)
	// inject a verifier key to confirm it lands in the page
	// (testServer builds the Server without one; hit the handler directly)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Is anyone checking it?") {
		t.Fatal("the overview is not served at / — a stranger still lands on a namespace text box")
	}
	// unsubstituted placeholder must not leak when no key configured
	if strings.Contains(string(body), "{{VERIFIER_KEY}}") {
		// empty replacement still removes the token
		t.Fatal("verifier-key placeholder left unsubstituted")
	}
}

func TestDocsServed(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /docs status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sequenceDiagram") || !strings.Contains(string(body), "Test cases") {
		t.Fatal("docs page missing diagram or test cases")
	}
	// expanded sections: API reference, self-host quickstart, protocol references
	for _, want := range []string{
		"/dedi/lookup/{namespace}/{registry}/{record}",
		"/dedi/log/proof/consistency?old=",
		"proof=inclusion",
		"Run your own node",
		"github.com/theflywheel/DeDi-node",
		"https://github.com/LF-Decentralized-Trust-labs/DeDi",
		"https://github.com/beckn-one/beckn-onix",
		"https://c2sp.org/tlog-checkpoint",
		"https://c2sp.org/signed-note",
		"rfc6962",
		"golang.org/x/mod/sumdb/tlog",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("docs page missing %q", want)
		}
	}
}

// The pages are embedded, so every node ships the same markup: the "Demo" tab
// has to be substituted per node or each one sends its visitors to the flywheel
// reference demo. Neither page may leak the raw placeholder either way.
func TestDemoURLSubstituted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		demoURL string
		want    string
	}{
		{"operator configured", "https://schemes.example.org/", "https://schemes.example.org/"},
		{"unset falls back", "", defaultDemoURL},
		{"escapes attribute metacharacters", `https://example.com/" onclick="alert(1)`, "https://example.com/&#34; onclick=&#34;alert(1)"},
		{"rejects non-http schemes", "javascript:alert(1)", defaultDemoURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{DemoURL: tc.demoURL}
			for path, h := range map[string]http.HandlerFunc{"/": s.explorer, "/docs": s.docs} {
				rec := httptest.NewRecorder()
				h(rec, httptest.NewRequest(http.MethodGet, path, nil))
				body := rec.Body.String()
				if !strings.Contains(body, `<a href="`+tc.want+`">Demo</a>`) {
					t.Errorf("%s: demo link not pointed at %q", path, tc.want)
				}
				if strings.Contains(body, "{{DEMO_URL}}") {
					t.Errorf("%s: demo placeholder left unsubstituted", path)
				}
			}
		})
	}
}

func TestUnknownRouteStill404AfterExplorer(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/not-a-route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status %d want 404", resp.StatusCode)
	}
}

// pageRoutes are every HTML page this server serves. Kept here rather than
// derived from the mux because the point of the test is to notice when the two
// disagree.
var pageRoutes = []string{"/", "/browse", "/verify", "/docs", "/admin"}

var navHref = regexp.MustCompile(`<nav>(.*?)</nav>`)

// Deliberately narrow. index.html and verify.html contain a literal "{{" in a
// JS guard against an unsubstituted key, so matching bare braces would fail on
// correct pages — and a test that cries wolf gets deleted rather than fixed.
var placeholderRe = regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
var hrefRe = regexp.MustCompile(`href="([^"]*)"`)

// A reader must be able to get from any page to any other. Before the nav was
// generated, four pages carried four different hardcoded navs and /verify — the
// page where you check this node's claims yourself — was linked from nowhere
// except itself. That is the failure this asserts against, and it asserts the
// outcome (you can get there) rather than the mechanism (a string got replaced).
func TestEveryPageIsReachableFromEveryOtherPage(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")

	for _, from := range pageRoutes {
		resp, err := http.Get(srv.URL + from)
		if err != nil {
			t.Fatalf("GET %s: %v", from, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", from, resp.StatusCode)
		}

		nav := navHref.FindSubmatch(body)
		if nav == nil {
			t.Errorf("%s: serves no <nav> at all", from)
			continue
		}
		found := map[string]bool{}
		for _, m := range hrefRe.FindAllSubmatch(nav[1], -1) {
			found[string(m[1])] = true
		}
		for _, to := range pageRoutes {
			if !found[to] {
				t.Errorf("%s does not link to %s — a reader landing there cannot reach it", from, to)
			}
		}
		// Every internal link in the nav must actually answer.
		for href := range found {
			if !strings.HasPrefix(href, "/") {
				continue // the Demo link points off this node
			}
			r2, err := http.Get(srv.URL + href)
			if err != nil {
				t.Fatalf("GET %s (linked from %s): %v", href, from, err)
			}
			r2.Body.Close()
			if r2.StatusCode != http.StatusOK {
				t.Errorf("%s links to %s, which answers %d", from, href, r2.StatusCode)
			}
		}
	}
}

// The substitution is a single bytes.Replace per placeholder, which fails
// silently: a page added without the placeholder, or a typo in one, serves
// perfectly valid HTML with a hole in it. This is what catches that.
func TestNoPlaceholderSurvivesIntoAServedPage(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")
	for _, p := range pageRoutes {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if m := placeholderRe.Find(body); m != nil {
			t.Errorf("%s served an unsubstituted placeholder: %q", p, string(m))
		}
	}
}

// The console must not be advertised where it is not served. A read-only node
// answers 404 rather than 401 at /admin so it does not advertise a door it does
// not have, and a nav entry pointing at that 404 would undo the same reasoning.
func TestNavOmitsTheConsoleOnAReadOnlyNode(t *testing.T) {
	srv, _, _ := testServer(t) // no publisher keys
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	nav := navHref.FindSubmatch(body)
	if nav == nil {
		t.Fatal("no nav served")
	}
	if strings.Contains(string(nav[1]), `href="/admin"`) {
		t.Error("a read-only node advertises /admin, which it answers 404 for")
	}
}

// The browser kept its function and lost its address. Nothing deep-linked into
// it (browsing state was never in the URL), so this is the whole of the move.
func TestBrowserServedAtBrowse(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/browse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /browse: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Browse the directory") {
		t.Fatal("the directory browser is not served at /browse")
	}
}

// The pages this node serves about itself must never count as directory
// traffic. The browser moved from "/" to "/browse" and took its exemption with
// it — for a while it did not, and every operator refresh inflated the figure
// the overview publishes as "requests served".
func TestNodesOwnPagesAreNotCountedAsDirectoryTraffic(t *testing.T) {
	for _, p := range []string{"/", "/browse", "/browse/", "/docs", "/docs/witnessing",
		"/verify", "/admin", "/metrics", "/healthz", "/dedi/stats", "/dedi/network"} {
		if !selfTraffic(p) {
			t.Errorf("%s counts as public directory traffic, but it is this node talking about itself", p)
		}
	}
	// The converse, so the exemption cannot quietly swallow the real thing.
	for _, p := range []string{"/dedi/lookup/ns/reg/rec", "/dedi/query/ns", "/dedi/witness"} {
		if selfTraffic(p) {
			t.Errorf("%s is directory traffic and must be counted", p)
		}
	}
}
