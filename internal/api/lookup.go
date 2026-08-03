package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func parseLookupParams(r *http.Request) (*int64, *time.Time, error) {
	q := r.URL.Query()
	var versionID *int64
	var asOn *time.Time
	if v := q.Get("version_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, nil, errors.New("version_id must be an integer version id")
		}
		versionID = &n
	}
	if v := q.Get("as_on"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil, nil, errors.New("as_on must be an RFC 3339 timestamp")
		}
		asOn = &t
	}
	return versionID, asOn, nil
}

// resolveWithVersions resolves the requested version and the full version
// list of a resource; used by every lookup handler.
func (s *Server) resolveWithVersions(w http.ResponseWriter, r *http.Request, entryType, ns, reg, rec, what string) (store.Entry, []store.Entry, bool) {
	vid, asOn, err := parseLookupParams(r)
	if err != nil {
		badRequest(w, err.Error())
		return store.Entry{}, nil, false
	}
	e, err := s.Store.Resolve(r.Context(), entryType, ns, reg, rec, vid, asOn)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, what)
		return store.Entry{}, nil, false
	}
	if err != nil {
		internal(w, err)
		return store.Entry{}, nil, false
	}
	versions, err := s.Store.Versions(r.Context(), entryType, ns, reg, rec)
	if err != nil {
		internal(w, err)
		return store.Entry{}, nil, false
	}
	return e, versions, true
}

func (s *Server) lookupNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	e, versions, okRes := s.resolveWithVersions(w, r, "namespace", ns, "", "", "namespace")
	if !okRes {
		return
	}
	s.respondLookup(w, r, "Namespace retrieved successfully", namespaceData(e, versions, s.TTL), e)
}

func (s *Server) lookupRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	e, versions, okRes := s.resolveWithVersions(w, r, "registry", ns, reg, "", "registry")
	if !okRes {
		return
	}
	s.respondLookup(w, r, "Registry retrieved successfully", registryData(e, versions, s.TTL), e)
}

// becknWildcardRegistry is ONIX dediregistry's hardcoded "search all
// registries" segment (beckn-onix pkg/plugin/implementation/dediregistry).
const becknWildcardRegistry = "subscribers.beckn.one"

func (s *Server) lookupRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	vid, asOn, err := parseLookupParams(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	e, err := s.Store.Resolve(r.Context(), "record", ns, reg, rec, vid, asOn)
	if errors.Is(err, store.ErrNotFound) && reg == becknWildcardRegistry && vid == nil && asOn == nil {
		e, err = s.Store.FindBecknSubscriber(r.Context(), ns, rec, s.WildcardNamespaces)
	}
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "record")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if revokedAndUnresolvable(r, e, vid, asOn) {
		notFound(w, "record")
		return
	}
	versions, err := s.Store.Versions(r.Context(), "record", e.Namespace, e.Registry, e.RecordName)
	if err != nil {
		internal(w, err)
		return
	}
	s.respondLookup(w, r, "Record retrieved successfully", recordData(e, versions, s.TTL), e)
}

// revokedAndUnresolvable reports whether this read must not resolve because
// the record's current version is revoked.
//
// Resolving a record means asking what it currently binds to. A revoked
// participant binds to nothing: continuing to answer with its url and
// signing_public_key is what revocation exists to stop. The Beckn wildcard
// path (store.FindBecknSubscriber) has always filtered on state, but the
// direct three-part path did not — and that is the path ONIX's LookupNode
// uses, which reads neither `state` nor `status` and treats any 200 as a live
// participant. A revoked BPP therefore kept getting routed to, and kept having
// its signatures validated, indefinitely. Only a non-200 stops it.
//
// History stays fully reachable, because this only gates the "what is it now"
// read. A version-pinned read (?version_id= / ?as_on=) is a question about the
// past and still answers, as does ?include_revoked=true and /dedi/versions.
// Nothing is hidden — only the live binding is withdrawn.
func revokedAndUnresolvable(r *http.Request, e store.Entry, vid *int64, asOn *time.Time) bool {
	if e.State != "revoked" || vid != nil || asOn != nil {
		return false
	}
	return r.URL.Query().Get("include_revoked") != "true"
}

// setCacheHeaders emits ETag/Cache-Control and answers 304 when the caller
// already holds the current version (design.md §5.4).
//
// A version-pinned read (?version_id= / ?as_on=) can never change, so it is
// immutable and cacheable for a long time. A latest-version read must stay
// short-lived: it is how a revocation reaches a consumer.
//
// This governs HTTP caches and proxies. It does NOT govern the ONIX
// dediregistry client, which keeps its own redis cache and honours the `ttl`
// field in the response body instead — see effectiveTTL.
//
// Returns true when it has written a 304 and the caller should stop.
func (s *Server) setCacheHeaders(w http.ResponseWriter, r *http.Request, e store.Entry) bool {
	q := r.URL.Query()
	pinned := q.Get("version_id") != "" || q.Get("as_on") != ""

	// The digest covers the payload; state and the proof mode are not in it but
	// do change the response, so they are part of the tag.
	etag := `"` + versionTag(e)
	if q.Get("proof") != "" {
		etag += "-proof"
	}
	etag += `"`
	w.Header().Set("ETag", etag)
	if pinned {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(effectiveTTL(e.PayloadRaw, s.TTL)))
	}

	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(candidate) == etag {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

// respondLookup emits the plain envelope, or attaches an inclusion proof
// when the caller passes ?proof=inclusion.
func (s *Server) respondLookup(w http.ResponseWriter, r *http.Request, msg string, data any, e store.Entry) {
	if s.setCacheHeaders(w, r, e) {
		return // client's copy is current; 304 already written
	}
	switch r.URL.Query().Get("proof") {
	case "":
		ok(w, msg, data)
	case "inclusion":
		p, err := s.buildProof(r, e)
		if err != nil {
			internal(w, err)
			return
		}
		okProof(w, msg, data, p)
	default:
		badRequest(w, "proof must be 'inclusion'")
	}
}
