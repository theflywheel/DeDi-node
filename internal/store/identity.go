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
// The insert and the conflict handler are one statement because two replicas
// booting against an empty database is the normal case on a platform that
// starts several containers at once. Doing this as SELECT-then-INSERT would let
// both see no row, both generate, and both believe they own the identity the
// other stored. ON CONFLICT uses a no-op update so the loser waits for the
// winner and still gets the winner's key back from RETURNING.
func (s *Store) EnsureIdentity(ctx context.Context, skey, vkey string) (string, string, error) {
	var gotSKey, gotVKey string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO node_identity (id, skey, vkey) VALUES (TRUE, $1, $2)
		ON CONFLICT (id) DO UPDATE
			SET skey = node_identity.skey,
				vkey = node_identity.vkey
		RETURNING skey, vkey`, skey, vkey).Scan(&gotSKey, &gotVKey)
	if err != nil {
		return "", "", err
	}
	return gotSKey, gotVKey, nil
}
