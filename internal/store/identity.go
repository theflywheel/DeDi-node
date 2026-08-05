package store

import "context"

// EnsureIdentity claims the node's signing identity, storing the offered pair
// only if the node does not already have one, and returning whichever identity
// is now authoritative.
//
// The offered key is a candidate, never a command: a node that adopted a fresh
// identity on some later boot would repudiate every checkpoint it had already
// signed, and every witness watching it would see an origin it does not
// recognise. So the first identity to land wins permanently, and callers are
// expected to use what comes back rather than what they sent.
//
// The insert and the read are one statement because two replicas booting
// against an empty database is the normal case on a platform that starts
// several containers at once. Doing this as SELECT-then-INSERT would let both
// see no row, both generate, and both believe they own the identity the other
// stored. ON CONFLICT DO NOTHING makes the loser's insert a no-op, and the
// UNION ALL arm then hands it the winner's key.
func (s *Store) EnsureIdentity(ctx context.Context, skey, vkey string) (string, string, error) {
	var gotSKey, gotVKey string
	err := s.pool.QueryRow(ctx, `
		WITH claimed AS (
			INSERT INTO node_identity (id, skey, vkey) VALUES (TRUE, $1, $2)
			ON CONFLICT (id) DO NOTHING
			RETURNING skey, vkey
		)
		SELECT skey, vkey FROM claimed
		UNION ALL
		SELECT skey, vkey FROM node_identity WHERE id = TRUE
		LIMIT 1`, skey, vkey).Scan(&gotSKey, &gotVKey)
	if err != nil {
		return "", "", err
	}
	return gotSKey, gotVKey, nil
}
