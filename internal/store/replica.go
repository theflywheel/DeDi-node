package store

import (
	"context"
	"fmt"
	"time"
)

// Support for running the log as a replicated state machine. A replica's
// database is a materialised view of an ordered command stream, so it must be
// possible to read that view back out (to capture a snapshot) and to rebuild
// it from nothing (to restore one).

// AllEntries returns every log entry in sequence order. This is the
// replicated state in full: tree hashes are derivable from it, and rebuilding
// them on restore is a free check that Apply really is deterministic.
//
// It materialises the whole log. That is deliberate and it is bounded by what
// design.md says a directory is — "small, public, read-heavy datasets" — but
// it is the assumption to revisit first if a deployment ever outgrows it.
func (s *Store) AllEntries(ctx context.Context) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+entryCols+` FROM log_entries ORDER BY seq ASC`)
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
	return out, rows.Err()
}

// Restore rebuilds this replica's view from a captured snapshot, discarding
// whatever was here before.
//
// Entries are replayed through Apply rather than inserted verbatim, so the
// tree is recomputed rather than copied. If the recomputed leaf hash differs
// from the captured one the snapshot and this build disagree about the leaf
// encoding, and restoring would hand back a replica that silently serves a
// different tree — so it is checked, and it is fatal.
// appliedIndex is the Raft index the snapshot was taken at; it is restored
// with the state so the rebuilt replica does not replay the commands the
// snapshot already contains.
// The whole restore is one transaction. It used to truncate on the pool and
// then replay entry by entry, each in its own transaction, which meant a
// snapshot that failed halfway — a bad snapshot, schema drift, a dropped
// connection — left the replica with its previous state permanently deleted and
// a prefix of the new one committed. That is the worst outcome available: not a
// replica that failed to restore, but one that is silently serving a truncated
// log. Either the replacement lands whole or nothing moves.
func (s *Store) Restore(ctx context.Context, entries []Entry, checkpoints []Checkpoint, appliedIndex uint64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// One lock for the whole restore rather than one per entry: the replayed
	// entries are a contiguous log and must not interleave with anything else.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`TRUNCATE log_entries, tree_hashes, checkpoints, raft_applied`); err != nil {
		return fmt.Errorf("clear replica state: %w", err)
	}
	for _, e := range entries {
		in := AppendInput{
			EntryType:  e.EntryType,
			Namespace:  e.Namespace,
			Registry:   e.Registry,
			RecordName: e.RecordName,
			PayloadRaw: e.PayloadRaw,
			State:      e.State,
			CreatedBy:  e.CreatedBy,
			CreatedAt:  e.CreatedAt,
		}
		raw, err := prepareAppend(&in)
		if err != nil {
			return fmt.Errorf("replay entry %d: %w", e.Seq, err)
		}
		applied, err := applyLocked(ctx, tx, in, raw)
		if err != nil {
			return fmt.Errorf("replay entry %d: %w", e.Seq, err)
		}
		if applied.Seq != e.Seq || string(applied.LeafHash) != string(e.LeafHash) {
			return fmt.Errorf("replay of entry %d diverged: got seq %d leaf %x, snapshot had seq %d leaf %x",
				e.Seq, applied.Seq, applied.LeafHash, e.Seq, e.LeafHash)
		}
	}
	for _, c := range checkpoints {
		// A snapshot taken before Checkpoint carried CreatedAt decodes with the
		// zero time, and writing that would date every restored checkpoint to
		// year 1 — so a rolling upgrade, or a restore from an older snapshot,
		// would leave /status and the checkpoint-age metric reporting a history
		// that never happened. Falling back to now() is the pre-existing
		// behaviour: wrong by the restore delay, and still correctly ordered.
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx,
			// created_at explicitly: the column defaults to now(), which would
			// restamp every restored checkpoint with the moment this replica
			// joined and erase the signing history the snapshot carried.
			`INSERT INTO checkpoints (tree_size, root_hash, note_text, created_at) VALUES ($1,$2,$3,$4)`,
			c.TreeSize, c.RootHash, c.NoteText, c.CreatedAt); err != nil {
			return fmt.Errorf("restore checkpoint %d: %w", c.TreeSize, err)
		}
	}
	if err := recordApplied(ctx, tx, appliedIndex); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
