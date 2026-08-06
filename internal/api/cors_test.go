package api

import (
	"net/http"
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
