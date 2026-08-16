package api

import (
	"bytes"
	_ "embed"
	"html"
	"net/http"
	"net/url"
)

//go:embed static/index.html
var explorerHTML []byte

//go:embed static/docs.html
var docsHTML []byte

//go:embed static/admin.html
var adminHTML []byte

//go:embed static/verify.html
var verifyHTML []byte

// verifyJS holds the proof-checking primitives. It is served as a file rather
// than inlined into each page so both the explorer and the evidence page run the
// same implementation: two copies would eventually disagree, and the page that
// was wrong would still be showing green ticks.
//
//go:embed static/verify.js
var verifyJS []byte

// defaultDemoURL is the flywheel reference demo, used when the operator sets
// no DEDI_DEMO_URL of their own.
const defaultDemoURL = "https://schemes.proto.theflywheel.in/"

// explorer serves the embedded read-only registry browser at "/". The node's
// verifier key (if configured) is injected so the page can check the
// checkpoint signature in-browser; absent it, the page still recomputes
// inclusion proofs and simply reports the signature as unchecked.
func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(explorerHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	page = bytes.Replace(page, []byte("{{DEMO_URL}}"), []byte(s.demoHref()), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// demoURL is the target of the nav's "Demo" tab. Every node ships the same
// embedded pages, so without this each one points its visitors at the flywheel
// reference demo rather than at whatever the operator runs.
func (s *Server) demoURL() string {
	if s.DemoURL == "" {
		return defaultDemoURL
	}
	u, err := url.Parse(s.DemoURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return defaultDemoURL
	}
	return s.DemoURL
}

func (s *Server) demoHref() string {
	return html.EscapeString(s.demoURL())
}

// verify serves the evidence page: every witness claim with the bytes it rests
// on, recomputed in the reader's browser. It takes the verifier key for the same
// reason the explorer does — without it the page can display a checkpoint but
// not establish that this node signed it.
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(verifyHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	page = bytes.Replace(page, []byte("{{DEMO_URL}}"), []byte(s.demoHref()), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

func (s *Server) verifyScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(verifyJS)
}

// docs serves the embedded explainer page (sequence diagrams + test cases).
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(docsHTML, []byte("{{DEMO_URL}}"), []byte(s.demoHref()), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// admin serves the operator console: participant onboarding, key rotation and
// revocation. The page holds no privilege of its own — it reads the public
// endpoints, and writes are Ed25519-signed in the browser with a publisher key
// the operator pastes in, which is imported non-extractable and never stored.
//
// It is served only where a write plane exists, behind the operator gate when
// one is configured, and with a policy that assumes the pasted key is the thing
// worth stealing. See adminPageHeaders.
func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	adminPageHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(adminHTML)
}
