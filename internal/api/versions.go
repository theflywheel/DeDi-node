package api

import (
	"errors"
	"net/http"

	"github.com/theflywheel/DeDi-node/internal/store"
)

type versionsDTO struct {
	RegistryName string         `json:"registry_name,omitempty"`
	CreatedBy    string         `json:"created_by"`
	Schema       map[string]any `json:"schema,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	TotalVersions int           `json:"total_versions"`
	Versions     []string       `json:"versions"`
	TTL          int            `json:"ttl"`
}

func (s *Server) versionsFor(w http.ResponseWriter, r *http.Request, entryType, ns, reg, rec, what string) ([]store.Entry, bool) {
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
	versions, okRes := s.versionsFor(w, r, "namespace", ns, "", "", "namespace")
	if !okRes {
		return
	}
	ok(w, "Namespace versions retrieved successfully", buildVersionsDTO(versions, s.TTL))
}

// registrySchema fetches the latest registry version's schema for decoration.
func (s *Server) registrySchema(r *http.Request, ns, reg string) map[string]any {
	e, err := s.Store.Resolve(r.Context(), "registry", ns, reg, "", nil, nil)
	if err != nil {
		return map[string]any{}
	}
	return parseMeta(e.PayloadRaw).Schema
}

func (s *Server) versionsRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	versions, okRes := s.versionsFor(w, r, "registry", ns, reg, "", "registry")
	if !okRes {
		return
	}
	dto := buildVersionsDTO(versions, s.TTL)
	dto.RegistryName = reg
	dto.Schema = s.registrySchema(r, ns, reg)
	ok(w, "Registry versions retrieved successfully", dto)
}

func (s *Server) versionsRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	versions, okRes := s.versionsFor(w, r, "record", ns, reg, rec, "record")
	if !okRes {
		return
	}
	dto := buildVersionsDTO(versions, s.TTL)
	dto.Schema = s.registrySchema(r, ns, reg)
	ok(w, "Record versions retrieved successfully", dto)
}
