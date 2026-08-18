package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Exactly-once application of the replicated command stream.
//
// Raft tracks its applied index in memory and, on restart, replays every
// committed entry into the state machine. That is correct when the state
// machine is rebuilt from a snapshot, and wrong here: this replica's state
// lives in Postgres and outlives the process. Replaying would append every
// command a second time, and since a duplicate append is a *new version*
// rather than an error, it would succeed — quietly giving this replica more
// entries than its peers and a different root.
//
// So the applied index is persisted, and it is written in the same transaction
// as the state change. A separate write would leave a window where a crash
// loses one and keeps the other.
//
// Publisher writes happen to carry preconditions that would reject a replay on
// their own, which is enough to hide this bug in a test that only exercises
// them. Witness verdicts carry none, and would have diverged.

// AppliedIndex is the highest Raft log index this replica has applied.
func (s *Store) AppliedIndex(ctx context.Context) (uint64, error) {
	var idx int64
	err := s.pool.QueryRow(ctx, `SELECT applied_index FROM raft_applied WHERE id = TRUE`).Scan(&idx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return uint64(idx), err
}

func recordApplied(ctx context.Context, tx pgx.Tx, index uint64) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO raft_applied (id, applied_index, updated_at) VALUES (TRUE, $1, now())
		 ON CONFLICT (id) DO UPDATE SET applied_index = EXCLUDED.applied_index, updated_at = now()`,
		int64(index))
	return err
}

// alreadyApplied reports whether index has been consumed, reading under the
// write lock so the answer cannot change underneath the caller.
func alreadyApplied(ctx context.Context, tx pgx.Tx, index uint64) (bool, error) {
	var applied int64
	err := tx.QueryRow(ctx, `SELECT applied_index FROM raft_applied WHERE id = TRUE`).Scan(&applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return uint64(applied) >= index, nil
}

// ApplyReplicated applies a command from the replicated stream exactly once.
// The bool reports whether the command was skipped as already applied, which
// is the normal case for every entry a restarting replica replays.
func (s *Store) ApplyReplicated(ctx context.Context, index uint64, in AppendInput) (Entry, bool, error) {
	raw, err := prepareAppend(&in)
	if err != nil {
		return Entry{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return Entry{}, false, err
	}
	done, err := alreadyApplied(ctx, tx, index)
	if err != nil {
		return Entry{}, false, err
	}
	if done {
		return Entry{}, true, nil
	}
	e, err := applyLocked(ctx, tx, in, raw)
	if err != nil {
		// A deterministic rejection still consumes the index: every replica
		// rejects it identically, and leaving the index behind would mean
		// replaying the rejection forever after a restart.
		if isRejection(err) {
			if markErr := s.markApplied(ctx, index); markErr != nil {
				return Entry{}, false, fmt.Errorf("%w (and recording the index failed: %v)", err, markErr)
			}
		}
		return Entry{}, false, err
	}
	if err := recordApplied(ctx, tx, index); err != nil {
		return Entry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, false, err
	}
	return e, false, nil
}

// SaveCheckpointReplicated persists a signed tree head exactly once.
func (s *Store) SaveCheckpointReplicated(ctx context.Context, index int64, size int64, root []byte, noteText string, signedAt time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return false, err
	}
	done, err := alreadyApplied(ctx, tx, uint64(index))
	if err != nil {
		return false, err
	}
	if done {
		return true, nil
	}
	// A command written before this field existed replays with a zero time.
	// Writing that would date the checkpoint to year 1; falling back to now()
	// is the old behaviour, which is wrong only by the replay delay and stays
	// ordered correctly.
	if signedAt.IsZero() {
		signedAt = time.Now().UTC()
	}
	var existing []byte
	err = tx.QueryRow(ctx,
		// created_at is the LEADER's signing time, carried in the command. The
		// column defaults to now(), which would record when this replica
		// happened to apply the entry — so the same checkpoint would carry a
		// different time on every node, and /status would show a different
		// signing history depending on which replica you asked.
		`INSERT INTO checkpoints (tree_size, root_hash, note_text, created_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tree_size) DO UPDATE SET tree_size = checkpoints.tree_size
		 RETURNING root_hash`, size, root, noteText, signedAt).Scan(&existing)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(existing, root) {
		return false, fmt.Errorf("%w: size %d already signed with root %x, refusing to sign %x",
			ErrCheckpointFork, size, existing, root)
	}
	if err := recordApplied(ctx, tx, uint64(index)); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}

// markApplied advances the index on its own, for the case where the command
// itself made no state change.
func (s *Store) markApplied(ctx context.Context, index uint64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := recordApplied(ctx, tx, index); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isRejection(err error) bool {
	return errors.Is(err, ErrInvalidWrite) || errors.Is(err, ErrVersionConflict) ||
		errors.Is(err, ErrNotFound)
}
