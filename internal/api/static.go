package api

import (
	"bytes"
	_ "embed"
	"net/http"
)

//go:embed static/index.html
var explorerHTML []byte

//go:embed static/docs.html
var docsHTML []byte

// explorer serves the embedded read-only registry browser at "/". The node's
// verifier key (if configured) is injected so the page can check the
// checkpoint signature in-browser; absent it, the page still recomputes
// inclusion proofs and simply reports the signature as unchecked.
func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	page := bytes.Replace(explorerHTML, []byte("{{VERIFIER_KEY}}"), []byte(s.VerifierKey), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// docs serves the embedded explainer page (sequence diagrams + test cases).
func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(docsHTML)
}
