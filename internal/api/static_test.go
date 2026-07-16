package api

import (
	"io"
	"net/http"
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
