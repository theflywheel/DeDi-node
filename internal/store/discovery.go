package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Discovery: answering "who serves domain X", as opposed to "what key does this
// subscriber use".
//
// The distinction is the whole point of this file. FindBecknSubscriber (beckn.go)
// takes a subscriber_id the caller already knows and returns one record; it
// exists so a signature can be validated. Nothing here could answer a router
// asking who it should talk to, which is why the `url` field every participant
// record carries has never been read by anything.
//
// It also gives revocation a second meaning. Today a revoked participant stops
// being *verifiable*. With this it also stops being *returned as a destination* —
// and those are different protections: the first stops a forged message being
// accepted, the second stops a real one being sent somewhere it should not go.

// ServingDomain returns the live records in an eligible namespace that declare
// they serve the given domain, most recent first.
//
// Every gate FindBecknSubscriber applies is applied here, and for sharper
// reasons rather than merely for consistency:
//
//   - state='live', so a revoked participant disappears from discovery the
//     moment it is revoked. This is the behaviour the read is for.
//   - status SUBSCRIBED or absent, so a participant mid-onboarding or already
//     unsubscribed is not offered as somewhere to send business. Records that
//     declare no status still resolve; many seeds predate the field, and
//     dropping them would quietly take working participants off the network.
//   - the eligible-namespace allowlist, which matters *more* here than it does
//     for lookup. A wildcard lookup is at least anchored to a subscriber_id the
//     caller already believed in. A discovery caller has no such anchor: it is
//     asking to be told who exists, so anyone able to publish a record on this
//     node could otherwise insert themselves as a destination for any domain
//     and receive traffic meant for someone else.
//
// A nil eligible slice means no restriction, matching FindBecknSubscriber;
// serve() refuses that combination once publisher keys are configured.
//
// domain matches a scalar `"domain": "retail"` and membership of an array
// `"domain": ["retail", "mobility"]`. The spec permits both spellings, and a
// participant seeded with the other one must not silently become undiscoverable.
func (s *Store) ServingDomain(ctx context.Context, domain string, eligible []string, limit int) ([]Entry, error) {
	if strings.TrimSpace(domain) == "" {
		return nil, fmt.Errorf("%w: domain is required", ErrInvalidFilter)
	}
	if eligible != nil && len(eligible) == 0 {
		// An explicitly empty allowlist means nothing may answer. Postgres would
		// treat an empty array the same way, but being explicit keeps that from
		// depending on how the driver encodes it.
		return []Entry{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	// The array form is matched by containment so the GIN index can serve it.
	asArray, err := json.Marshal(map[string][]string{"domain": {domain}})
	if err != nil {
		return nil, err
	}

	q := `
WITH latest AS (
  SELECT DISTINCT ON (namespace, registry, record_name) ` + entryCols + `, payload
  FROM log_entries
  WHERE entry_type='record'
  ORDER BY namespace, registry, record_name, version_num DESC
)
SELECT ` + entryCols + ` FROM latest
WHERE state='live'
  AND (payload->>'domain' = $1 OR payload @> $2::jsonb)
  AND ($3::text[] IS NULL OR namespace = ANY($3::text[]))
  AND (payload->>'status' IS NULL OR payload->>'status' = 'SUBSCRIBED')
ORDER BY created_at DESC, seq DESC
LIMIT $4`

	rows, err := s.pool.Query(ctx, q, domain, string(asArray), eligible, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
