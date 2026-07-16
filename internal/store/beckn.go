package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// FindBecknSubscriber resolves the ONIX dediregistry wildcard lookup
// GET /dedi/lookup/{subject}/subscribers.beckn.one/{recordName}: the latest
// live version of a record named recordName whose namespace is subject or
// whose payload subscriber_id is subject. Namespace matches win, then
// recency. Only live records are visible to Beckn lookups — a revoked
// latest version hides the participant.
func (s *Store) FindBecknSubscriber(ctx context.Context, subject, recordName string) (Entry, error) {
	q := `
WITH latest AS (
  SELECT DISTINCT ON (namespace, registry) ` + entryCols + `, payload
  FROM log_entries
  WHERE entry_type='record' AND record_name=$2
  ORDER BY namespace, registry, version_num DESC
)
SELECT ` + entryCols + ` FROM latest
WHERE state='live' AND (namespace=$1 OR payload->>'subscriber_id'=$1)
ORDER BY (namespace=$1) DESC, created_at DESC, seq DESC
LIMIT 1`
	e, err := scanEntry(s.pool.QueryRow(ctx, q, subject, recordName))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}
