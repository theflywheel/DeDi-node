package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SaveCheckpoint persists a signed tree head. Re-saving an identical
// checkpoint is a no-op, which is what makes publishing idempotent.
//
// Saving a *different* root at a size already signed is refused with
// ErrCheckpointFork. This is the anti-double-sign fence, and it is the single
// most important invariant in a replicated deployment: two signed checkpoints
// at the same size with different roots is a fork under one identity key —
// exactly the tampering the witness ring exists to detect, and unrecoverable
// once published. Under Raft it should be unreachable, because committed
// entries are never lost and every replica applies the same ordered commands;
// if it ever fires, something deeper is wrong and stopping loudly is the only
// safe response. The primary key does the work.
func (s *Store) SaveCheckpoint(ctx context.Context, size int64, root []byte, noteText string) error {
	var existing []byte
	err := s.pool.QueryRow(ctx,
		`INSERT INTO checkpoints (tree_size, root_hash, note_text) VALUES ($1,$2,$3)
		 ON CONFLICT (tree_size) DO UPDATE SET tree_size = checkpoints.tree_size
		 RETURNING root_hash`, size, root, noteText).Scan(&existing)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, root) {
		return fmt.Errorf("%w: size %d already signed with root %x, refusing to sign %x",
			ErrCheckpointFork, size, existing, root)
	}
	return nil
}

// Checkpoint is a persisted signed tree head.
type Checkpoint struct {
	TreeSize int64
	RootHash []byte
	NoteText string
}

// AllCheckpoints returns every checkpoint in tree order. Used to capture
// replicated state: notes cannot be regenerated without the signing key, so a
// snapshot must carry them rather than expect a restoring replica to re-sign.
func (s *Store) AllCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tree_size, root_hash, note_text FROM checkpoints ORDER BY tree_size ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.TreeSize, &c.RootHash, &c.NoteText); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) LatestCheckpoint(ctx context.Context) (int64, string, error) {
	var size int64
	var text string
	err := s.pool.QueryRow(ctx,
		`SELECT tree_size, note_text FROM checkpoints ORDER BY tree_size DESC LIMIT 1`).Scan(&size, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNoCheckpoint
	}
	return size, text, err
}

// LatestCheckpointAt reports the size and publication time of the newest
// checkpoint. Callers that only need the note itself want LatestCheckpoint;
// this exists for health reporting, where when the log was last signed matters
// as much as what it says.
func (s *Store) LatestCheckpointAt(ctx context.Context) (int64, time.Time, error) {
	var size int64
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT tree_size, created_at FROM checkpoints ORDER BY tree_size DESC LIMIT 1`).Scan(&size, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, ErrNoCheckpoint
	}
	return size, at, err
}

// TruncateForTest empties all log state. Test support only.
func (s *Store) TruncateForTest(ctx context.Context) error {
	// node_identity is included because it is log state too: the key that signed
	// the checkpoints being truncated has no meaning once they are gone, and a
	// surviving identity would silently carry into the next test's fresh log.
	_, err := s.pool.Exec(ctx, `TRUNCATE log_entries, tree_hashes, checkpoints, request_counts, node_identity`)
	return err
}
