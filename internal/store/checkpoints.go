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
	// CreatedAt is when the checkpoint was SIGNED, not when this database row
	// happened to be written. The column defaults to now(), so a replica
	// restoring a snapshot used to stamp every checkpoint it had ever received
	// with the instant it joined — collapsing a node's whole signing history
	// into one moment, and making the status page read it as an outage.
	CreatedAt time.Time
}

// AllCheckpoints returns every checkpoint in tree order. Used to capture
// replicated state: notes cannot be regenerated without the signing key, so a
// snapshot must carry them rather than expect a restoring replica to re-sign.
func (s *Store) AllCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tree_size, root_hash, note_text, created_at FROM checkpoints ORDER BY tree_size ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.TreeSize, &c.RootHash, &c.NoteText, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HistoryBucket is one slice of this node's past: how much was written, how
// much was signed, and — the part that matters — the highest log position
// reached inside it.
//
// MaxSeq is what makes a fault decidable without trusting clocks. A checkpoint
// covers a PREFIX of the log, so any entry at or below the latest checkpoint's
// tree size is signed, whenever either happened. Deciding "these writes went
// unsigned" by whether a checkpoint landed in the same time bucket compares two
// clocks and a bucket boundary, and gets it wrong routinely: steady-state
// signing is one tick after the write it covers, which is often the next
// bucket. Position answers the question exactly.
type HistoryBucket struct {
	At          time.Time
	Checkpoints int64
	Entries     int64
	MaxSeq      int64
}

// History returns the last n buckets ending now, newest last.
//
// Both series are aggregated here, on one grid, in one query, because the page
// that reads them must line them up and any second grid is a source of
// disagreement. An earlier version bucketed entries in SQL and checkpoints in
// the browser: the two grids were offset by however far into a bucket the
// request happened to arrive, so a write and the checkpoint that signed it
// landed in different cells about a third of the time, and a perfectly healthy
// node was painted with the one colour this page uses for a genuine fault.
//
// Counts rather than rows, so there is no row cap to truncate and no way for a
// busy node to report a quiet one.
func (s *Store) History(ctx context.Context, buckets int, bucket time.Duration) ([]HistoryBucket, error) {
	if buckets <= 0 || buckets > 500 {
		buckets = 48
	}
	if bucket < time.Minute {
		bucket = 30 * time.Minute
	}
	secs := int64(bucket.Seconds())
	rows, err := s.pool.Query(ctx, `
WITH grid AS (
  SELECT to_timestamp((floor(extract(epoch FROM now()) / $1) - g) * $1) AS at
    FROM generate_series(0, $2 - 1) AS g
), e AS (
  SELECT to_timestamp(floor(extract(epoch FROM created_at) / $1) * $1) AS at,
         count(*) AS n, max(seq) AS max_seq
    FROM log_entries
   WHERE created_at >= (SELECT min(at) FROM grid)
   GROUP BY 1
), c AS (
  SELECT to_timestamp(floor(extract(epoch FROM created_at) / $1) * $1) AS at, count(*) AS n
    FROM checkpoints
   WHERE created_at >= (SELECT min(at) FROM grid)
   GROUP BY 1
)
SELECT grid.at, COALESCE(c.n, 0), COALESCE(e.n, 0), COALESCE(e.max_seq, -1)
  FROM grid LEFT JOIN e ON e.at = grid.at LEFT JOIN c ON c.at = grid.at
 ORDER BY grid.at ASC`, secs, buckets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HistoryBucket, 0, buckets)
	for rows.Next() {
		var b HistoryBucket
		if err := rows.Scan(&b.At, &b.Checkpoints, &b.Entries, &b.MaxSeq); err != nil {
			return nil, err
		}
		out = append(out, b)
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
	_, err := s.pool.Exec(ctx, `TRUNCATE log_entries, tree_hashes, checkpoints, request_counts, node_identity, raft_applied,
		webhook_dead_letters, webhook_subscriptions`)
	return err
}
