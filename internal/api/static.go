package api

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html"
	"net/http"
	"net/url"

	"github.com/theflywheel/DeDi-node/internal/provision"
)

//go:embed static/index.html
var explorerHTML []byte

//go:embed static/overview.html
var overviewHTML []byte

//go:embed static/network.html
var networkPageHTML []byte

//go:embed static/status.html
var statusPageHTML []byte

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

// overview serves the front door at "/": what this node is, what it holds, and
// whether anyone independent is checking it.
//
// It exists because "/" used to do three unrelated jobs at once — node identity,
// the whole network and cluster panel, and the namespace/registry/record
// browser. A stranger arriving at a directory node was greeted by a namespace
// text box, which answers a question they had not yet worked out how to ask.
// The browser now lives at /browse, where someone who already knows what they
// are looking for goes.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(overviewHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	s.writePage(w, page, "/")
}

// explorer serves the embedded read-only registry browser at "/browse". The
// node's verifier key (if configured) is injected so the page can check the
// checkpoint signature in-browser; absent it, the page still recomputes
// inclusion proofs and simply reports the signature as unchecked.
func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(explorerHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	s.writePage(w, page, "/browse")
}

// networkPage serves the ring: who watches whom across this deployment, and
// what each of them has actually proved.
//
// It takes the verifier key for the same reason the other pages do — with it a
// reader's browser can check a checkpoint signature rather than accepting this
// node's summary of one.
func (s *Server) networkPage(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(networkPageHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	s.writePage(w, page, "/network")
}

// writePage substitutes the placeholders every page shares and sends it.
//
// The nav is generated here rather than written into each file (see nav.go):
// four hardcoded copies had already drifted into four different navs, and the
// page a reader most needs — /verify — was linked from nowhere but itself.
func (s *Server) writePage(w http.ResponseWriter, page []byte, current string) {
	page = bytes.Replace(page, []byte("{{DEMO_URL}}"), []byte(s.demoHref()), 1)
	page = bytes.Replace(page, []byte("{{NAV}}"), []byte(s.nav(current)), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// replaceOnce substitutes a placeholder, returning a fresh slice so the
// embedded page is never mutated.
func replaceOnce(page []byte, token, with string) []byte {
	return bytes.Replace(page, []byte(token), []byte(with), 1)
}

// externalStatusHref names the outside monitor that watches this node from
// somewhere else.
//
// It has to be configurable and it has to default to nothing. Hardcoding the
// flywheel demo's monitor would have every deployment of this binary assert
// that an external monitor it does not run, and cannot see, is watching it —
// on the one page whose thesis is that it can only report what it signed.
func (s *Server) externalStatusHref() string {
	if s.StatusURL == "" {
		return ""
	}
	u, err := url.Parse(s.StatusURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return html.EscapeString(s.StatusURL)
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
	s.writePage(w, page, "/verify")
}

func (s *Server) verifyScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(verifyJS)
}

// docs serves the embedded explainer page (sequence diagrams + test cases).
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	page := docsHTML
	// The contents list is generated from what this build actually embeds, not
	// written into the page. A hand-maintained index is a list of links that
	// stops matching the documents the moment someone adds one.
	page = bytes.Replace(page, []byte("{{DOC_INDEX}}"), []byte(s.docIndex()), 1)
	s.writePage(w, page, "/docs")
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
	// The role catalogue is rendered into the page rather than fetched,
	// because the picker needs it before anything has been created — it was
	// previously only in the response to creating a child, so the picker would
	// have been empty on the page where you choose what to create.
	//
	// It goes into a non-executing <script type="application/json"> block, not
	// a JavaScript string literal. encoding/json escapes <, > and & in strings,
	// so nothing in a role description can close that block.
	cat, err := json.Marshal(provision.RoleCatalogue())
	if err != nil {
		internal(w, err)
		return
	}
	s.writePage(w, replaceOnce(adminHTML, "{{ROLE_CATALOGUE}}", string(cat)), "/admin")
}
