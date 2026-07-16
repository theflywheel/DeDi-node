package api

import (
	"errors"
	"net/http"
	"strconv"
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
		e, err = s.Store.FindBecknSubscriber(r.Context(), ns, rec)
	}
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "record")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	versions, err := s.Store.Versions(r.Context(), "record", e.Namespace, e.Registry, e.RecordName)
	if err != nil {
		internal(w, err)
		return
	}
	s.respondLookup(w, r, "Record retrieved successfully", recordData(e, versions, s.TTL), e)
}

// respondLookup emits the plain envelope, or attaches an inclusion proof
// when the caller passes ?proof=inclusion.
func (s *Server) respondLookup(w http.ResponseWriter, r *http.Request, msg string, data any, e store.Entry) {
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
