package api

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func parseQueryFilters(r *http.Request) (store.QueryFilters, error) {
	q := r.URL.Query()
	var f store.QueryFilters
	if v := q.Get("name"); v != "" {
		f.Name = &v
	}
	// spec: `status` on namespace query, `state` on registry query
	if v := q.Get("status"); v != "" {
		f.State = &v
	}
	if v := q.Get("state"); v != "" {
		f.State = &v
	}
	for key, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To, "as_on": &f.AsOn} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return f, errors.New(key + " must be an RFC 3339 timestamp")
			}
			*dst = &t
		}
	}
	f.Sort = q.Get("sort")
	for key, dst := range map[string]*int{"page": &f.Page, "page_size": &f.PageSize} {
		if v := q.Get(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return f, errors.New(key + " must be a positive integer")
			}
			*dst = n
		}
	}
	return f, nil
}

type registrySummaryDTO struct {
	RegistryID   string `json:"registry_id"`
	RegistryName string `json:"registry_name"`
	Description  string `json:"description"`
	Digest       string `json:"digest"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	State        string `json:"state"`
	RecordCount  int    `json:"record_count"`
	TTL          int    `json:"ttl"`
}

type recordSummaryDTO struct {
	RecordID    string `json:"record_id"`
	RecordName  string `json:"record_name"`
	Digest      string `json:"digest"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	State       string `json:"state"`
	TTL         int    `json:"ttl"`
}

func (s *Server) queryNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	f, err := parseQueryFilters(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	// mirror store normalize() so the echoed page_size/total_pages match the applied LIMIT
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 25
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	nsEntry, err := s.Store.Resolve(r.Context(), "namespace", ns, "", "", nil, f.AsOn)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "namespace")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	rows, total, err := s.Store.QueryRegistries(r.Context(), ns, f)
	if err != nil {
		badRequest(w, err.Error()) // only filter validation errors surface here
		return
	}
	regs := make([]registrySummaryDTO, 0, len(rows))
	for _, row := range rows {
		regs = append(regs, registrySummaryDTO{
			RegistryID: ns + "/" + row.Name, RegistryName: row.Name,
			Digest:    hex.EncodeToString(row.Digest),
			CreatedAt: fmtTime(row.FirstAt), UpdatedAt: fmtTime(row.LatestAt),
			State: row.State, RecordCount: row.RecordCount, TTL: s.TTL,
		})
	}
	nsMeta := parseMeta(nsEntry.PayloadRaw)
	nsVersions, err := s.Store.Versions(r.Context(), "namespace", ns, "", "")
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, "Registries retrieved successfully", map[string]any{
		"namespace_id":     ns,
		"namespace_name":   ns,
		"domain":           nsMeta.Domain,
		"created_by":       nsEntry.CreatedBy,
		"created_at":       fmtTime(nsVersions[0].CreatedAt),
		"updated_at":       fmtTime(nsEntry.CreatedAt),
		"total_registries": total,
		"page_number":      f.Page,
		"page_size":        f.PageSize,
		"registries":       regs,
	})
}

func (s *Server) queryRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	f, err := parseQueryFilters(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 25
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	regEntry, err := s.Store.Resolve(r.Context(), "registry", ns, reg, "", nil, f.AsOn)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "registry")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	rows, total, err := s.Store.QueryRecords(r.Context(), ns, reg, f)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	recs := make([]recordSummaryDTO, 0, len(rows))
	for _, row := range rows {
		recs = append(recs, recordSummaryDTO{
			RecordID: ns + "/" + reg + "/" + row.Name, RecordName: row.Name,
			Digest:    hex.EncodeToString(row.Digest),
			CreatedAt: fmtTime(row.FirstAt), UpdatedAt: fmtTime(row.LatestAt),
			State: row.State, TTL: s.TTL,
		})
	}
	regMeta := parseMeta(regEntry.PayloadRaw)
	regVersions, err := s.Store.Versions(r.Context(), "registry", ns, reg, "")
	if err != nil {
		internal(w, err)
		return
	}
	totalPages := 0
	if f.PageSize > 0 {
		totalPages = (total + f.PageSize - 1) / f.PageSize
	}
	ok(w, "Records retrieved successfully", map[string]any{
		"namespace_id":   ns,
		"namespace_name": ns,
		"registry_id":    ns + "/" + reg,
		"registry_name":  reg,
		"schema":         regMeta.Schema,
		"meta":           regMeta.Meta,
		"created_by":     regEntry.CreatedBy,
		"created_at":     fmtTime(regVersions[0].CreatedAt),
		"updated_at":     fmtTime(regEntry.CreatedAt),
		"total_records":  total,
		"total_pages":    totalPages,
		"page_number":    f.Page,
		"page_size":      f.PageSize,
		"records":        recs,
	})
}
