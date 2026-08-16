package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const entryCols = `seq, entry_type, namespace, registry, record_name, version_num,
	payload_raw, digest, state, created_by, created_at, leaf_hash`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(&e.Seq, &e.EntryType, &e.Namespace, &e.Registry, &e.RecordName, &e.VersionNum,
		&e.PayloadRaw, &e.Digest, &e.State, &e.CreatedBy, &e.CreatedAt, &e.LeafHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

// Resolve fetches one version of a resource: by versionID (seq) if given,
// else latest as of asOn if given, else latest.
func (s *Store) Resolve(ctx context.Context, entryType, ns, reg, rec string, versionID *int64, asOn *time.Time) (Entry, error) {
	q := `SELECT ` + entryCols + ` FROM log_entries
	      WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4`
	args := []any{entryType, ns, reg, rec}
	switch {
	case versionID != nil:
		q += ` AND seq=$5`
		args = append(args, *versionID)
	case asOn != nil:
		q += ` AND created_at <= $5 ORDER BY version_num DESC LIMIT 1`
		args = append(args, *asOn)
	default:
		q += ` ORDER BY version_num DESC LIMIT 1`
	}
	return scanEntry(s.pool.QueryRow(ctx, q, args...))
}

// ResolveCurrentForWrite reads the current resource version under the append
// lock and applies the write precondition to the same serialized view.
func (s *Store) ResolveCurrentForWrite(ctx context.Context, in AppendInput) (Entry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return Entry{}, err
	}
	e, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+entryCols+` FROM log_entries
		  WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4
		  ORDER BY version_num DESC LIMIT 1`,
		in.EntryType, in.Namespace, in.Registry, in.RecordName))
	if err != nil {
		return Entry{}, err
	}
	if err := checkPrecondition(in, e.Digest, e.State); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Versions returns every version of a resource in ascending version order.
func (s *Store) Versions(ctx context.Context, entryType, ns, reg, rec string) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entryCols+` FROM log_entries
		WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4
		ORDER BY version_num ASC`, entryType, ns, reg, rec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Seq, &e.EntryType, &e.Namespace, &e.Registry, &e.RecordName, &e.VersionNum,
			&e.PayloadRaw, &e.Digest, &e.State, &e.CreatedBy, &e.CreatedAt, &e.LeafHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}
