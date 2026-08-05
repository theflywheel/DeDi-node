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

// docs serves the embedded explainer page (sequence diagrams + test cases).
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(docsHTML, []byte("{{DEMO_URL}}"), []byte(s.demoHref()), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// admin serves the operator console: participant onboarding, key rotation and
// revocation. The page is read-only against the node — it drives the public read
// endpoints and generates the seed file an operator applies out of band, because
// dedid exposes no write API (docs/governance.md).
func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(adminHTML)
}
