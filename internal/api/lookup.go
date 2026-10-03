package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// errNoSuchVersion marks a version_id that cannot name any version of
// anything, as distinct from one that names a version we do not have.
//
// The spec types version_id as an unconstrained string (openapi.yaml) — no
// format, no pattern — so "abc" is a well-formed request under the published
// contract even though our version ids are log sequence numbers. Answering 400
// told a conformant client its request was malformed when it was not. Both
// values are simply versions that do not exist here, and both now resolve to
// 404. Found by conformance/, which reads the parameter's declared type rather
// than assuming it.
var errNoSuchVersion = errors.New("no such version")

// lookupParams is every query key a lookup route reads. Anything else is
// rejected rather than ignored (#65): ?versionId=2 used to return the latest
// version with a valid proof attached, and every check a careful client runs
// passed, because they are all over the record the node chose to return.
//
// internal is read by internalNamespaceGuard, not here, so it is easy to miss.
var lookupParams = map[string]bool{
	"version_id": true, "as_on": true, "proof": true, "include_revoked": true, "internal": true,
}

func parseLookupParams(r *http.Request) (*int64, *time.Time, error) {
	// Not r.URL.Query(): it discards ParseQuery's error along with every
	// segment that has a ';' or a bad %-escape, so ?versionId=2; never reached
	// the check below and answered with the latest version all the same.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed query string: %v", err)
	}
	for k, vs := range q {
		// Every reader below takes the first value, so ?version_id=&version_id=2
		// would unpin silently, the #65 failure reached through a duplicate.
		if len(vs) > 1 && lookupParams[k] {
			return nil, nil, fmt.Errorf("query parameter %q given more than once", k)
		}
		if !lookupParams[k] {
			accepted := make([]string, 0, len(lookupParams))
			for p := range lookupParams {
				accepted = append(accepted, p)
			}
			sort.Strings(accepted)
			return nil, nil, fmt.Errorf("unknown query parameter %q; lookups accept %s", k, strings.Join(accepted, ", "))
		}
	}
	var versionID *int64
	var asOn *time.Time
	if v := q.Get("version_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, nil, errNoSuchVersion
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
	if errors.Is(err, errNoSuchVersion) {
		notFound(w, what)
		return store.Entry{}, nil, false
	}
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
	if internalNamespaceGuard(w, r, ns, "namespace") {
		return
	}
	e, versions, okRes := s.resolveWithVersions(w, r, "namespace", ns, "", "", "namespace")
	if !okRes {
		return
	}
	s.respondLookup(w, r, "Namespace retrieved successfully", namespaceData(e, versions, s.TTL), e)
}

func (s *Server) lookupRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	if internalNamespaceGuard(w, r, ns, "registry") {
		return
	}
	e, versions, okRes := s.resolveWithVersions(w, r, "registry", ns, reg, "", "registry")
	if !okRes {
		return
	}
	s.respondLookup(w, r, "Registry retrieved successfully", registryData(e, versions, s.TTL), e)
}

// becknWildcardRegistry is ONIX dediregistry's hardcoded "search all
// registries" segment (beckn-onix pkg/plugin/implementation/dediregistry).
const becknWildcardRegistry = "subscribers.beckn.one"

// becknNamespaceEligible reports whether a namespace may answer as authority
// for beckn subscriber identity.
//
// A nil allowlist is no restriction, which is the shape a node has when its
// write plane is closed: with nobody able to publish, there is nothing to scope.
// Configuring publisher keys makes DEDI_WILDCARD_NAMESPACES mandatory, so the
// check only binds where it has something to protect against.
//
// This governs the unversioned read — what a subscriber binds to *now*, which
// is what routing consumes. Asking for a specific version or an as-on time is
// reading history, and history is not an identity claim.
func (s *Server) becknNamespaceEligible(ns string) bool {
	if s.WildcardNamespaces == nil {
		return true
	}
	for _, n := range s.WildcardNamespaces {
		if n == ns {
			return true
		}
	}
	return false
}

func (s *Server) lookupRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	if internalNamespaceGuard(w, r, ns, "record") {
		return
	}
	vid, asOn, err := parseLookupParams(r)
	if errors.Is(err, errNoSuchVersion) {
		notFound(w, "record")
		return
	}
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	e, err := s.Store.Resolve(r.Context(), "record", ns, reg, rec, vid, asOn)
	if reg == becknWildcardRegistry && vid == nil && asOn == nil {
		// An exact hit here used to be returned whatever namespace it sat in,
		// so the eligibility allowlist only ever guarded the fallback. With the
		// write plane open that is a hole: a publisher scoped to a namespace
		// named after someone else's subscriber_id could create
		// subscribers.beckn.one/{key_id} under it and answer ONIX lookups for
		// an identity it does not hold, because ONIX reads any 200 on this path
		// as a live participant.
		//
		// So an ineligible exact hit is not authoritative and does not short
		// the search — the eligible namespaces still get their chance to answer,
		// which is what FindBecknSubscriber is for.
		if errors.Is(err, store.ErrNotFound) || !s.becknNamespaceEligible(ns) {
			e, err = s.Store.FindBecknSubscriber(r.Context(), ns, rec, s.WildcardNamespaces)
		}
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
