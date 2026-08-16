package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplorerServedAtRoot(t *testing.T) {
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
	if !strings.Contains(string(body), "DeDi Node Explorer") {
		t.Fatal("explorer markup not served at /")
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
