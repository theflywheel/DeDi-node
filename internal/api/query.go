package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// queryParams is every key a /dedi/query listing reads (#73). The spec gives
// status to the namespace route and state to the registry route; both are read
// on both, so both are accepted on both. internal is read by
// internalNamespaceGuard.
var queryParams = map[string]bool{
	"name": true, "status": true, "state": true, "from": true, "to": true, "as_on": true,
	"sort": true, "page": true, "page_size": true, "internal": true,
}

// domainQueryParams is what the discovery branch reads. It answers the domain
// question alone, so a filter or page sent with it would be ignored rather
// than applied.
var domainQueryParams = map[string]bool{"domain": true, "internal": true}

func parseQueryFilters(r *http.Request) (store.QueryFilters, error) {
	q, err := strictQuery(r, queryParams)
	if err != nil {
		return store.QueryFilters{}, err
	}
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
			t, err := parseDateParam(key, v, key != "from")
			if err != nil {
				return f, err
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
	if internalNamespaceGuard(w, r, ns, "namespace") {
		return
	}
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
	if errors.Is(err, store.ErrInvalidFilter) {
		badRequest(w, err.Error())
		return
	}
	if err != nil {
		internal(w, err)
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
	if internalNamespaceGuard(w, r, ns, "registry") {
		return
	}

	// The discovery extension (design.md §120). Opt-in by the presence of the
	// parameter, so /dedi/query without it behaves exactly as it always has and
	// still never reaches into the payload.
	if domain := r.URL.Query().Get("domain"); domain != "" {
		if _, err := strictQuery(r, domainQueryParams); err != nil {
			badRequest(w, err.Error())
			return
		}
		s.queryByDomain(w, r, ns, reg, domain)
		return
	}

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
	if errors.Is(err, store.ErrInvalidFilter) {
		badRequest(w, err.Error())
		return
	}
	if err != nil {
		internal(w, err)
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

// parseDateParam reads a date query parameter (as_on, from, to).
//
// The spec declares all of them `format: date`, YYYY-MM-DD (#69). We only took
// RFC 3339, so the spec's own format got a 400 -- including the plainest way to
// ask what a record said on a given day. Both are accepted now: RFC 3339 for
// the callers already sending it, and a bare date read as a whole UTC day.
//
// Every comparison against these is inclusive (store: created_at <= as_on,
// latest_at between from and to), so a day means its first instant for a lower
// bound and its last for an upper one. as_on=2026-03-01 is the version in force
// at the close of 1 March, not at the midnight it began.
func parseDateParam(key, v string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	d, err := time.Parse(time.DateOnly, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be a date (YYYY-MM-DD) or an RFC 3339 timestamp", key)
	}
	if endOfDay {
		// Postgres keeps microseconds, so this is the last instant it can store.
		return d.AddDate(0, 0, 1).Add(-time.Microsecond), nil
	}
	return d, nil
}
