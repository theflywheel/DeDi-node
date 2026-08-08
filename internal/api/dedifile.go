package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
)

// dedifileFreshness is how far past "now" every DeDi file and the manifest
// this node emits declares its next_update. Short enough that a crawler
// following docs/publishing-dedi-files.md section 9 treats a stalled node as
// stale within the hour rather than trusting a silently unreachable copy for
// a day.
const dedifileFreshness = time.Hour

// signerForDedifile derives this node's DeDi-file signing key from its
// existing checkpoint identity (internal/dedifile.SignerFromNodeKey), so a
// relying party that already trusts this node's checkpoints needs no second
// credential to trust its DeDi files. Empty, false when the node has no
// identity key yet — e.g. mid-startup — which the caller reports as 503
// rather than serving an unsigned or wrongly-keyed file.
func (s *Server) signerForDedifile() (dedifile.Config, bool) {
	if s.CP == nil || s.CP.SKey == "" {
		return dedifile.Config{}, false
	}
	priv, kid, err := dedifile.SignerFromNodeKey(s.CP.SKey)
	if err != nil {
		return dedifile.Config{}, false
	}
	return dedifile.Config{Signer: priv, Kid: kid}, true
}

// dedifileOrigin resolves this node's publisher identity for the file-
// publication artifacts: a base URL (scheme://host, no trailing slash) and
// the bare domain embedded in publisher.domain / manifest.domain.
//
// PublicURL is preferred — the same "how does this node know its own
// address" config the child-enrolment callback URL uses (see cmd/dedid
// serve()), and for the same reason: behind a proxy the request's own Host
// is the proxy's, not ours. Without it, the request's Host is the best
// available fallback, matching what a directly-reached node would compute.
func (s *Server) dedifileOrigin(r *http.Request) (baseURL, domain string) {
	if s.PublicURL != "" {
		base := strings.TrimRight(s.PublicURL, "/")
		host := base
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		return base, host
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host, r.Host
}

// writeJSONWithETag serves v as application/json with an ETag derived from
// its own content digest, and honours a matching If-None-Match with 304 —
// the conditional-fetch behaviour docs/publishing-dedi-files.md §14 flags as
// desirable for a high-churn manifest, and free to offer for every DeDi file
// besides.
func writeJSONWithETag(w http.ResponseWriter, r *http.Request, v any, nextUpdate string) {
	raw, err := json.Marshal(v)
	if err != nil {
		internal(w, err)
		return
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	if cc, ok := cacheControlUntil(nextUpdate, time.Now()); ok {
		w.Header().Set("Cache-Control", cc)
	}
	// Public directory data, same posture as the rest of the read plane; a
	// browser-based verifier needs this to fetch the file at all (spec §5.2).
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// cacheControlUntil derives a Cache-Control header that expires no later than
// the artifact's own declared next_update.
//
// Spec §5.2: a publisher SHOULD NOT advertise a max-age extending beyond
// next_update, because "an HTTP cache outliving that bound would serve copies
// the protocol has already declared stale." Freshness is declared in-band, so
// the HTTP layer must not promise more than the document itself does.
//
// Returns false when next_update is unparseable or already past — an artifact
// that is stale on arrival gets no max-age at all rather than a negative or
// zero one, leaving revalidation to the ETag.
func cacheControlUntil(nextUpdate string, now time.Time) (string, bool) {
	t, err := time.Parse(time.RFC3339, nextUpdate)
	if err != nil {
		return "", false
	}
	secs := int(t.Sub(now).Seconds())
	if secs <= 0 {
		return "", false
	}
	return fmt.Sprintf("public, max-age=%d", secs), true
}

// wellKnownIndex serves the signed manifest at /.well-known/dedi.index.json
// (RFC 8615), the fixed, normative path docs/publishing-dedi-files.md §6
// requires. It lists every DeDi file this node currently publishes.
func (s *Server) wellKnownIndex(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.signerForDedifile()
	if !ok {
		http.Error(w, "node identity key not available", http.StatusServiceUnavailable)
		return
	}
	cfg.BaseURL, cfg.Domain = s.dedifileOrigin(r)
	cfg.Freshness = dedifileFreshness
	manifest, _, err := dedifile.Build(r.Context(), s.Store, cfg)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSONWithETag(w, r, manifest, manifest.NextUpdate)
}

// dedifileByNamespace serves one DeDi file: GET
// /dedi-files/{namespace}/dedi.{registry}.json. Filename and directory are
// RECOMMENDED conventions, not normative (spec §5.2); this path is what the
// manifest's files[].url points at, and following it is all a verifier
// needs to do.
//
// Registered off /dedi-files/ rather than under /dedi/ — the /dedi/ prefix
// is this node's existing API surface (docs/spec/lfdt/api/openapi.yaml), and
// the spec's own suggested /dedi/ directory for files is only RECOMMENDED,
// so reusing our API prefix for a different artifact would collide instead
// of aligning.
func (s *Server) dedifileByNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	if internalNamespaceGuard(w, r, ns, "namespace") {
		return
	}
	filename := r.PathValue("file")
	registry, ok := registryFromFilename(filename)
	if !ok {
		notFound(w, "dedi file")
		return
	}
	cfg, ok := s.signerForDedifile()
	if !ok {
		http.Error(w, "node identity key not available", http.StatusServiceUnavailable)
		return
	}
	cfg.BaseURL, cfg.Domain = s.dedifileOrigin(r)
	cfg.Freshness = dedifileFreshness
	_, files, err := dedifile.Build(r.Context(), s.Store, cfg)
	if err != nil {
		internal(w, err)
		return
	}
	for _, f := range files {
		if f.Namespace == ns && f.Registry.Name == registry {
			writeJSONWithETag(w, r, f, f.NextUpdate)
			return
		}
	}
	notFound(w, "dedi file")
}

// registryFromFilename extracts "foo" from the RECOMMENDED "dedi.foo.json"
// filename convention (spec §5.2). Go's ServeMux cannot express a literal
// prefix/suffix and a wildcard within one path segment, so the {file}
// wildcard captures the whole segment and this parses it by hand.
func registryFromFilename(filename string) (registry string, ok bool) {
	const prefix, suffix = "dedi.", ".json"
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return "", false
	}
	registry = strings.TrimSuffix(strings.TrimPrefix(filename, prefix), suffix)
	if registry == "" {
		return "", false
	}
	return registry, true
}
