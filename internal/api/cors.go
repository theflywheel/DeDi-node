package api

import (
	"net/http"
	"strings"
)

// readPlaneCORS allows any origin to read the public directory endpoints.
//
// This is what makes cross-node verification possible in a browser. A visitor
// looking at node A's page should be able to check A's claim about node B by
// fetching B's checkpoint and consistency proofs directly from B — if the only
// party that can talk to B is A itself, the visitor is back to taking A's word
// for it, which is precisely the trust the design refuses to require.
//
// The read plane is public, unauthenticated, and identical for every caller, so
// there is nothing here for a cross-origin read to leak: an attacker's page can
// already fetch these bytes from its own server. The write plane is deliberately
// excluded — its routes authenticate each request by signature rather than by
// ambient credentials, so cross-origin access would not forge a write, but there
// is no reason to widen a surface that no browser needs.
func readPlaneCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isReadPlane(r.URL.Path) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			// Proofs and checkpoints are safely cacheable per-origin, but the
			// header varies by nothing — say so explicitly so intermediaries do
			// not invent a Vary of their own.
			w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
		}
		next.ServeHTTP(w, r)
	})
}

// isReadPlane reports whether a path is part of the public, unauthenticated read
// surface. /admin is excluded even though it is a GET: it is the operator
// console, not directory data.
func isReadPlane(path string) bool {
	return path == "/healthz" || path == "/dedi" || strings.HasPrefix(path, "/dedi/")
}
