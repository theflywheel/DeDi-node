package store

import (
	"context"
	"fmt"
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
func (s *Store) Restore(ctx context.Context, entries []Entry, checkpoints []Checkpoint, appliedIndex uint64) error {
	if _, err := s.pool.Exec(ctx,
		`TRUNCATE log_entries, tree_hashes, checkpoints, raft_applied`); err != nil {
		return fmt.Errorf("clear replica state: %w", err)
	}
	for _, e := range entries {
		applied, err := s.Apply(ctx, AppendInput{
			EntryType:  e.EntryType,
			Namespace:  e.Namespace,
			Registry:   e.Registry,
			RecordName: e.RecordName,
			PayloadRaw: e.PayloadRaw,
			State:      e.State,
			CreatedBy:  e.CreatedBy,
			CreatedAt:  e.CreatedAt,
		})
		if err != nil {
			return fmt.Errorf("replay entry %d: %w", e.Seq, err)
		}
		if applied.Seq != e.Seq || string(applied.LeafHash) != string(e.LeafHash) {
			return fmt.Errorf("replay of entry %d diverged: got seq %d leaf %x, snapshot had seq %d leaf %x",
				e.Seq, applied.Seq, applied.LeafHash, e.Seq, e.LeafHash)
		}
	}
	for _, c := range checkpoints {
		if err := s.SaveCheckpoint(ctx, c.TreeSize, c.RootHash, c.NoteText); err != nil {
			return fmt.Errorf("restore checkpoint %d: %w", c.TreeSize, err)
		}
	}
	return s.markApplied(ctx, appliedIndex)
}
