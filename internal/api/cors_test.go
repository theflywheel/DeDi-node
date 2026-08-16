package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadPlaneIsCrossOriginReadable(t *testing.T) {
	srv, _, _ := testServer(t)

	// Cross-node verification happens in a visitor's browser: looking at node A,
	// it must be able to fetch node B's checkpoint and proofs from B directly.
	// Without this header the browser can only ask A about B, which is exactly
	// the trust the design refuses to require.
	for _, path := range []string{"/dedi/log/checkpoint", "/dedi/query/_witness", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want \"*\"", path, got)
		}
	}
}

func TestOperatorConsoleIsNotCrossOriginReadable(t *testing.T) {
	srv, _, _ := testServer(t)

	// The console is a GET but it is not directory data, and no browser on
	// another origin has any reason to read it.
	for _, path := range []string{"/admin", "/"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: unexpectedly cross-origin readable (%q)", path, got)
		}
	}
}

func TestIsReadPlaneDoesNotMatchOnPrefixAlone(t *testing.T) {
	// "/dedirect" starts with "/dedi" but is not the read plane; matching on a
	// bare prefix would quietly widen the surface with any future route.
	for path, want := range map[string]bool{
		"/dedi/lookup/ns": true,
		"/dedi":           true,
		"/healthz":        true,
		"/dedirect":       false,
		"/admin":          false,
		"/":               false,
	} {
		if got := isReadPlane(path); got != want {
			t.Errorf("isReadPlane(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestVerificationCodeIsServedOnceForBothPages(t *testing.T) {
	srv, _, _ := testServer(t)

	// Both pages must pull the same file. If either ever carries its own copy of
	// the proof-checking code, the two can drift and the wrong one still renders
	// green ticks.
	for _, page := range []string{"/", "/verify"} {
		resp, err := http.Get(srv.URL + page)
		if err != nil {
			t.Fatalf("%s: %v", page, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Contains(body, []byte(`src="/static/verify.js"`)) {
			t.Errorf("%s does not load the shared verifier", page)
		}
		// The tell-tale of an inlined copy.
		if bytes.Contains(body, []byte("async function proofRoot(")) {
			t.Errorf("%s carries its own copy of the proof verifier", page)
		}
	}

	resp, err := http.Get(srv.URL + "/static/verify.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/static/verify.js: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type %q — a browser will refuse to execute it", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, fn := range []string{"function checkTree", "async function proofRoot",
		"function leafBytes", "async function checkTreeTraced"} {
		if !bytes.Contains(body, []byte(fn)) {
			t.Errorf("verify.js is missing %q", fn)
		}
	}
}

func TestEvidencePageDoesNotCountAsServedTraffic(t *testing.T) {
	// It polls nothing, but it is this node's own page, like /docs and /admin.
	for _, p := range []string{
		"/verify", "/verify/", "/static/verify.js", "/docs/witnessing",
	} {
		if !selfTraffic(p) {
			t.Errorf("%s should not count as served traffic", p)
		}
	}
}
