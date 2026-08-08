package api

import "net/http"

// hideInternalNamespace reports whether the spec read endpoints
// (/dedi/lookup, /dedi/query, /dedi/versions) must treat ns as not-found.
//
// Namespaces prefixed with `_` are this node's own bookkeeping (e.g.
// `_witness`, see internal/witness/witness.go), never created through the
// spec's write path. Left visible, a spec-only crawler indexes them as
// ordinary directory data. They stay reachable for the explorer/verify UI
// and other internal callers via the escape hatch `?internal=1`.
func hideInternalNamespace(r *http.Request, ns string) bool {
	if len(ns) == 0 || ns[0] != '_' {
		return false
	}
	return r.URL.Query().Get("internal") != "1"
}

// internalNamespaceGuard 404s the request and reports whether it did, for
// use as the first line of a namespace-rooted read handler.
func internalNamespaceGuard(w http.ResponseWriter, r *http.Request, ns, what string) bool {
	if hideInternalNamespace(r, ns) {
		notFound(w, what)
		return true
	}
	return false
}
