package api

import (
	"errors"
	"net/http"

	"github.com/theflywheel/DeDi-node/internal/store"
)

type versionsDTO struct {
	RegistryName  string   `json:"registry_name,omitempty"`
	CreatedBy     string   `json:"created_by"`
	Schema        any      `json:"schema,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	TotalVersions int      `json:"total_versions"`
	Versions      []string `json:"versions"`
	TTL           int      `json:"ttl"`
}

// versionsParams is every key a /dedi/versions route reads: none of its own,
// and internal, read by internalNamespaceGuard (#73).
var versionsParams = map[string]bool{"internal": true}

func (s *Server) versionsFor(w http.ResponseWriter, r *http.Request, entryType, ns, reg, rec, what string) ([]store.Entry, bool) {
	if _, err := strictQuery(r, versionsParams); err != nil {
		badRequest(w, err.Error())
		return nil, false
	}
	versions, err := s.Store.Versions(r.Context(), entryType, ns, reg, rec)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, what)
		return nil, false
	}
	if err != nil {
		internal(w, err)
		return nil, false
	}
	return versions, true
}

func buildVersionsDTO(versions []store.Entry, ttl int) versionsDTO {
	first, last := versions[0], versions[len(versions)-1]
	ids := make([]string, len(versions))
	for i, v := range versions {
		ids[i] = versionID(v.Seq)
	}
	return versionsDTO{
		CreatedBy: first.CreatedBy,
		CreatedAt: fmtTime(first.CreatedAt), UpdatedAt: fmtTime(last.CreatedAt),
		TotalVersions: len(versions), Versions: ids, TTL: ttl,
	}
}

func (s *Server) versionsNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	if internalNamespaceGuard(w, r, ns, "namespace") {
		return
	}
	versions, okRes := s.versionsFor(w, r, "namespace", ns, "", "", "namespace")
	if !okRes {
		return
	}
	ok(w, "Namespace versions retrieved successfully", buildVersionsDTO(versions, s.TTL))
}

// registrySchema fetches the latest registry version's schema for decoration.
// A missing registry degrades to an empty schema; any other Resolve error is
// surfaced to the caller as a genuine failure.
func (s *Server) registrySchema(r *http.Request, ns, reg string) (map[string]any, error) {
	e, err := s.Store.Resolve(r.Context(), "registry", ns, reg, "", nil, nil)
	if errors.Is(err, store.ErrNotFound) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseMeta(e.PayloadRaw).Schema, nil
}

func (s *Server) versionsRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	if internalNamespaceGuard(w, r, ns, "registry") {
		return
	}
	versions, okRes := s.versionsFor(w, r, "registry", ns, reg, "", "registry")
	if !okRes {
		return
	}
	dto := buildVersionsDTO(versions, s.TTL)
	dto.RegistryName = reg
	schema, err := s.registrySchema(r, ns, reg)
	if err != nil {
		internal(w, err)
		return
	}
	dto.Schema = schema
	ok(w, "Registry versions retrieved successfully", dto)
}

func (s *Server) versionsRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	if internalNamespaceGuard(w, r, ns, "record") {
		return
	}
	versions, okRes := s.versionsFor(w, r, "record", ns, reg, rec, "record")
	if !okRes {
		return
	}
	dto := buildVersionsDTO(versions, s.TTL)
	schema, err := s.registrySchema(r, ns, reg)
	if err != nil {
		internal(w, err)
		return
	}
	dto.Schema = schema
	ok(w, "Record versions retrieved successfully", dto)
}
