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
//
// eligible restricts which namespaces may answer a wildcard lookup — the
// constraint design.md:256 makes binding on the publisher plane. Because the
// wildcard searches every namespace for a record of the given name, without it
// anyone able to publish a record named {key_id} anywhere on the node can
// answer for any subscriber_id, and the ONIX client does not check that the
// returned subscriber_id is the one it asked for. A nil slice means no
// restriction, which is safe only while writes are operator-only; serve()
// refuses that combination once publisher keys are configured.
//
// The filter lives here rather than in the handler so no future caller can
// reach the wildcard query without passing through it.
func (s *Store) FindBecknSubscriber(ctx context.Context, subject, recordName string, eligible []string) (Entry, error) {
	q := `
WITH latest AS (
  SELECT DISTINCT ON (namespace, registry) ` + entryCols + `, payload
  FROM log_entries
  WHERE entry_type='record' AND record_name=$2
  ORDER BY namespace, registry, version_num DESC
)
SELECT ` + entryCols + ` FROM latest
WHERE state='live' AND (namespace=$1 OR payload->>'subscriber_id'=$1)
  AND ($3::text[] IS NULL OR namespace = ANY($3::text[]))
ORDER BY (namespace=$1) DESC, created_at DESC, seq DESC
LIMIT 1`
	if eligible != nil && len(eligible) == 0 {
		// An explicitly empty allowlist means nothing is eligible. Postgres
		// would treat an empty array as "matches nothing" too, but being
		// explicit keeps that from depending on driver encoding.
		return Entry{}, ErrNotFound
	}
	e, err := scanEntry(s.pool.QueryRow(ctx, q, subject, recordName, eligible))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}
