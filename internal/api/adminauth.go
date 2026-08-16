package api

import (
	"crypto/subtle"
	"net/http"
)

// AdminAuth is a deployment-level gate on the admin surface: the operator of
// this node, as distinct from the publishers whose keys sign individual writes.
//
// It is additive, never a replacement. The signature on a write is what makes
// the write attributable — it names a key id, it is scoped to the namespaces
// that key may touch, and it is what puts `publisher:<kid>` on the version. A
// shared password can do none of that, so it is a gate on *reaching* the
// surface rather than an authority over what happens on it. Both must pass.
type AdminAuth struct {
	User     string
	Password string
	// Realm appears in the browser's prompt. The console is a browser client,
	// so this is the operator-facing name of the thing asking.
	Realm string
}

// gate wraps a handler so it is reachable only with the configured credentials.
// A nil *AdminAuth is no gate at all, which is the shape a node has when the
// operator configured none.
func (a *AdminAuth) gate(h http.Handler) http.Handler {
	if a == nil || a.Password == "" {
		return h
	}
	realm := a.Realm
	if realm == "" {
		realm = "dedid admin"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		// Compare both halves unconditionally and combine at the end: returning
		// early on a username miss leaks, by timing, which half was wrong.
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.User))
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(a.Password))
		if !ok || userOK&passOK != 1 {
			// The challenge is what makes the console usable: the browser
			// prompts once and then carries the header on the page's own
			// fetches to the admin API, so the gate covers both without the
			// page having to manage a credential itself.
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED",
				"this node's admin surface requires operator credentials")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// adminPageHeaders hardens the console against having its one real secret taken
// out of the browser.
//
// The page asks an operator to paste a publisher private key. That key is
// imported non-extractable and kept in a closure, never stored — but a script
// injected into this origin, or a clickjacked iframe of this form, would be a
// publisher key compromise rather than a defaced page. The page is entirely
// self-contained apart from its own /static/verify.js, so a strict policy costs
// nothing to serve.
func adminPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; "+
			"frame-ancestors 'none'; object-src 'none'")
	// X-Frame-Options for anything that predates frame-ancestors.
	w.Header().Set("X-Frame-Options", "DENY")
	// A pasted key never belongs in a URL, but the namespace and registry names
	// the operator browses do end up in one, and they are this node's business.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Nothing about an operator console should be in a shared cache.
	w.Header().Set("Cache-Control", "no-store")
}
