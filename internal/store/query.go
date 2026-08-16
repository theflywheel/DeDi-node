package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrInvalidFilter marks query filter validation errors (e.g. an unknown
// sort key) so callers can distinguish them from genuine store failures.
var ErrInvalidFilter = errors.New("invalid query filter")

type QueryFilters struct {
	Name     *string
	State    *string
	From     *time.Time
	To       *time.Time
	AsOn     *time.Time
	Sort     string // "", date, status, name, id
	Page     int    // 1-based
	PageSize int
}

type SummaryRow struct {
	Name        string
	Digest      []byte
	State       string
	FirstAt     time.Time
	LatestAt    time.Time
	RecordCount int
}

// sortExprs whitelists ORDER BY fragments; keys are the spec's sort values.
// Secondary key `name` keeps pagination deterministic.
var sortExprs = map[string]string{
	"":       "latest_at DESC, name ASC",
	"date":   "latest_at DESC, name ASC",
	"name":   "name ASC",
	"id":     "name ASC",
	"status": "state ASC, name ASC",
}

// validStates whitelists the state/status filter value. The spec's own
// enums are inconsistent (docs/conformance.md nit #1): namespace-query
// `status` is constrained to [active, inactive] and registry-query `state`
// to [live], but the entities we actually store use [active, archived,
// revoked] (namespaces/registries) and [draft, live, suspended, revoked,
// expired] (records). `inactive` never appears as a stored state. Rather
// than pick one side and break either spec-conformant clients or existing
// working queries, we validate against the union of both: the spec's
// query enums plus every state value we ever write.
var validStates = map[string]bool{
	// spec query enums (openapi.yaml:255, 369)
	"active":   true,
	"inactive": true,
	"live":     true,
	// stored entity states (openapi.yaml:706, 741, 784)
	"archived":  true,
	"revoked":   true,
	"suspended": true,
	"expired":   true,
	"draft":     true,
}

func (f *QueryFilters) normalize() (orderBy string, limit, offset int, err error) {
	orderBy, okSort := sortExprs[f.Sort]
	if !okSort {
		return "", 0, 0, fmt.Errorf("invalid sort %q: %w", f.Sort, ErrInvalidFilter)
	}
	if f.State != nil && !validStates[*f.State] {
		return "", 0, 0, fmt.Errorf("invalid state %q: %w", *f.State, ErrInvalidFilter)
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
	return orderBy, f.PageSize, (f.Page - 1) * f.PageSize, nil
}

// Two deliberately separate query constants — same shape, different name
// column and record_count decoration. Only the ORDER BY fragment is
// interpolated, and only from the sortExprs whitelist.
const registrySummaryQuery = `
WITH latest AS (
  SELECT DISTINCT ON (registry) registry AS name, digest, state, created_at AS latest_at
  FROM log_entries
  WHERE entry_type='registry' AND namespace=$1
    AND ($2::timestamptz IS NULL OR created_at <= $2)
  ORDER BY registry, version_num DESC
), filtered AS (
  SELECT l.*,
    (SELECT MIN(g.created_at) FROM log_entries g
      WHERE g.entry_type='registry' AND g.namespace=$1 AND g.registry = l.name) AS first_at
  FROM latest l
  WHERE ($3::text IS NULL OR l.name ILIKE '%%'||$3||'%%')
    AND ($4::text IS NULL OR l.state = $4)
    AND ($5::timestamptz IS NULL OR l.latest_at >= $5)
    AND ($6::timestamptz IS NULL OR l.latest_at <= $6)
)
SELECT f.name, f.digest, f.state, f.first_at, f.latest_at,
  (SELECT COUNT(*) FROM filtered) AS total,
  (SELECT COUNT(DISTINCT r.record_name) FROM log_entries r
    WHERE r.entry_type='record' AND r.namespace=$1 AND r.registry = f.name) AS record_count
FROM filtered f
ORDER BY %s
LIMIT $7 OFFSET $8`

const recordSummaryQuery = `
WITH latest AS (
  SELECT DISTINCT ON (record_name) record_name AS name, digest, state, created_at AS latest_at
  FROM log_entries
  WHERE entry_type='record' AND namespace=$1 AND registry=$2
    AND ($3::timestamptz IS NULL OR created_at <= $3)
  ORDER BY record_name, version_num DESC
), filtered AS (
  SELECT l.*,
    (SELECT MIN(g.created_at) FROM log_entries g
      WHERE g.entry_type='record' AND g.namespace=$1 AND g.registry=$2 AND g.record_name = l.name) AS first_at
  FROM latest l
  WHERE ($4::text IS NULL OR l.name ILIKE '%%'||$4||'%%')
    AND ($5::text IS NULL OR l.state = $5)
    AND ($6::timestamptz IS NULL OR l.latest_at >= $6)
    AND ($7::timestamptz IS NULL OR l.latest_at <= $7)
)
SELECT f.name, f.digest, f.state, f.first_at, f.latest_at,
  (SELECT COUNT(*) FROM filtered) AS total,
  0 AS record_count
FROM filtered f
ORDER BY %s
LIMIT $8 OFFSET $9`

func scanSummaries(rows pgx.Rows) ([]SummaryRow, int, error) {
	defer rows.Close()
	var out []SummaryRow
	total := 0
	for rows.Next() {
		var r SummaryRow
		if err := rows.Scan(&r.Name, &r.Digest, &r.State, &r.FirstAt, &r.LatestAt, &total, &r.RecordCount); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *Store) QueryRegistries(ctx context.Context, ns string, f QueryFilters) ([]SummaryRow, int, error) {
	orderBy, limit, offset, err := f.normalize()
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(registrySummaryQuery, orderBy),
		ns, f.AsOn, f.Name, f.State, f.From, f.To, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return scanSummaries(rows)
}

func (s *Store) QueryRecords(ctx context.Context, ns, reg string, f QueryFilters) ([]SummaryRow, int, error) {
	orderBy, limit, offset, err := f.normalize()
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(recordSummaryQuery, orderBy),
		ns, reg, f.AsOn, f.Name, f.State, f.From, f.To, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return scanSummaries(rows)
}
