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
	// Case-insensitive: the heading became a lowercase eyebrow when the design
	// was applied, and an exact-string assertion turned a styling change into a
	// test failure about routing.
	if !strings.Contains(strings.ToLower(string(body)), "is anyone checking it") {
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
		"flywheelai/dedi-node",
		"https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol",
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
	// The source repository is private, so a reader of /docs cannot follow a
	// link into it or clone it: the page has to stand on what a reader can reach.
	for _, never := range []string{"github.com/theflywheel/DeDi-node", "git clone"} {
		if strings.Contains(string(body), never) {
			t.Errorf("docs page still points readers at %q, which they cannot reach", never)
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
var pageRoutes = []string{"/", "/browse", "/network", "/status", "/check", "/verify", "/docs", "/admin"}

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
	for _, p := range []string{"/", "/browse", "/browse/", "/network", "/network/", "/status", "/status/", "/check", "/check/",
		"/docs", "/docs/witnessing",
		"/verify", "/admin", "/metrics", "/healthz", "/dedi/stats", "/dedi/network", "/dedi/log/history"} {
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

// The ring page must load the shared proof verifier rather than carrying its
// own: it re-runs the consistency check in the reader's browser, and two copies
// of that code would eventually disagree while both showed green ticks.
func TestRingPageUsesTheSharedVerifier(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/network")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `src="/static/verify.js"`) {
		t.Error("/network does not load the shared verifier")
	}
	if strings.Contains(string(body), "async function proofRoot(") {
		t.Error("/network carries its own copy of the proof verifier")
	}
	// The three primitives the re-check depends on. A rename in verify.js would
	// otherwise leave the button throwing at runtime and nowhere else.
	for _, fn := range []string{"verifyCheckpointSig", "parseCheckpointBody", "checkTree", "parseNote", "b64d"} {
		if !strings.Contains(string(body), fn) {
			t.Errorf("/network never calls %s — the in-browser check cannot work", fn)
		}
	}
}

// Whatever the ring page calls, verify.js must actually define.
func TestSharedVerifierDefinesWhatTheRingPageCalls(t *testing.T) {
	for _, fn := range []string{"verifyCheckpointSig", "parseCheckpointBody", "checkTree", "parseNote", "b64d"} {
		if !strings.Contains(string(verifyJS), "function "+fn+"(") {
			t.Errorf("verify.js does not define %s, which /network calls", fn)
		}
	}
}

// The ring page must refuse to offer its in-browser re-check on an edge the
// witness has already caught. Equivocation is recorded with the NEW root
// (internal/witness/witness.go), so a browser checking against it passes — and
// would print a green tick beside the alarm. The browser never held the
// pre-rewrite root and structurally cannot reproduce the catch.
func TestRingPageRefusesToRecheckACaughtEdge(t *testing.T) {
	// The page no longer spells the test out: reading a verdict moved into the
	// shared classifier, because five pages had each open-coded it and got it
	// wrong differently. This asked for the literal string and would now fail on
	// a page whose behaviour is unchanged — a mechanism assertion outliving its
	// mechanism. What must remain true is that a caught edge cannot be
	// re-checked, so that is what this asks.
	page := string(networkPageHTML)
	if !strings.Contains(page, "caught") {
		t.Fatal("the ring page does not distinguish a caught edge at all")
	}
	i := strings.Index(page, "const canCheck")
	if i < 0 {
		t.Fatal("canCheck is gone; the guard this test protects has been restructured")
	}
	guard := page[i : i+240]
	// Sound is the only state that may be re-checked, and 'caught' is not it.
	if !strings.Contains(guard, "'sound'") && !strings.Contains(guard, "!caught") {
		t.Errorf("canCheck does not restrict the re-check to a sound verdict, so a caught edge "+
			"would render a green tick beside the alarm:\n%s", guard)
	}
}

// verifyCheckpointSig returns null when the browser cannot run the check at all
// (no Ed25519, or an insecure origin) and false only when a signature genuinely
// fails. Folding them together accuses every honest target of forgery.
func TestRingPageSeparatesCannotCheckFromCheckFailed(t *testing.T) {
	page := string(networkPageHTML)
	if !strings.Contains(page, "sigOK === null") {
		t.Error("the ring page does not distinguish 'could not check' from 'check failed'")
	}
	if !strings.Contains(page, "sigOK === false") {
		t.Error("the ring page does not test for a genuine signature failure")
	}
	if strings.Contains(page, "sigOK !== true") {
		t.Error("the ring page still folds null into the failure branch")
	}
}

var anyHref = regexp.MustCompile(`href="(/[^"{}]*)"`)

// Every internal link in every served page must resolve — not just the ones in
// the nav. This exists because the same mistake was made twice in two days: a
// link to /network written before that page existed, then a link to /status
// written before that one did. Both shipped through review as valid HTML
// pointing at a 404, and both were on pages whose whole purpose is to be
// checkable.
func TestEveryInternalLinkInEveryPageResolves(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")

	// Pages are fetched through the running server so generated markup (the
	// nav, the doc index) is included exactly as a reader receives it.
	pages := append([]string{}, pageRoutes...)
	pages = append(pages, "/docs/witnessing", "/docs/replication")

	seen := map[string]bool{}
	for _, from := range pages {
		resp, err := http.Get(srv.URL + from)
		if err != nil {
			t.Fatalf("GET %s: %v", from, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		for _, m := range anyHref.FindAllSubmatch(body, -1) {
			href := string(m[1])
			// Fragment-only and query-only links go nowhere new.
			if i := strings.IndexAny(href, "#?"); i > 0 {
				href = href[:i]
			}
			if href == "" || seen[from+" "+href] {
				continue
			}
			seen[from+" "+href] = true
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

// The two sources of a target's URL have different shapes and must not be
// concatenated the same way: /dedi/network publishes a node URL with no path,
// while a verdict's target_url already ends in /dedi (internal/witness sets
// TargetURL to the base "including /dedi"). Getting this wrong produces
// /dedi/dedi/log/checkpoint, which 404s only at runtime, in the browser, on the
// one button whose whole point is to work without trusting anyone.
func TestRingPageNormalisesTheTargetBase(t *testing.T) {
	page := string(networkPageHTML)
	if !strings.Contains(page, "function apiBase(") {
		t.Fatal("the ring page no longer routes target URLs through one place")
	}
	if !strings.Contains(page, "t.target_url") {
		t.Error("the ring page ignores the verdict's target_url, so an edge to a node discovered only " +
			"through a witness (a delegated child) cannot be checked")
	}
	// The bug this guards: building log paths with an extra /dedi.
	for _, bad := range []string{`'/dedi/log/checkpoint'`, `"/dedi/log/checkpoint"`,
		`'/dedi/log/proof/consistency`, `"/dedi/log/proof/consistency`} {
		if strings.Contains(page, "base + "+bad) {
			t.Errorf("the ring page appends %s to an already-/dedi base", bad)
		}
	}
}
