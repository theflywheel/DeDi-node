package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/raft"

	"github.com/theflywheel/DeDi-node/internal/store"
)

var errUnknownCommand = errors.New("unknown command kind")

// applyTimeout bounds a single state transition against the database. It is
// generous: exceeding it means the database is unwell, and the replica stops
// rather than skips (see Apply).
const applyTimeout = 30 * time.Second

// fsm materialises the replicated command stream into this replica's own
// database.
type fsm struct {
	store *store.Store
	// halt is called when the replica cannot apply a committed entry. Applying
	// is not optional: every replica must reach the same state, so a replica
	// that cannot must stop rather than continue with a divergent view.
	halt func(error)
}

// Apply runs one committed command. Its return value reaches the proposing
// leader through ApplyFuture.Response and is ignored everywhere else.
//
// The distinction that matters here is between two kinds of failure:
//
//   - A *deterministic rejection* — malformed input, a failed precondition, a
//     missing parent — is part of the state machine. Every replica reaches it
//     identically, state stays in step, and it is returned to the proposer as a
//     value so the client gets its 400 or 409.
//
//   - An *infrastructure failure* — the database is unreachable, a disk is
//     full — is not part of the state machine. Only this replica sees it, and
//     if it were swallowed this replica would skip an entry every other replica
//     applied and silently serve a different tree from then on. There is no
//     recovering from that quietly, so the replica halts.
//
// Conflating the two is the classic way to corrupt a replicated log, which is
// why the store distinguishes its error classes in the first place.
func (f *fsm) Apply(l *raft.Log) any {
	cmd, err := decodeCommand(l.Data)
	if err != nil {
		f.halt(fmt.Errorf("raft index %d: %w", l.Index, err))
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()

	switch cmd.Kind {
	case cmdAppend:
		entry, skipped, err := f.store.ApplyReplicated(ctx, l.Index, *cmd.Input)
		if skipped {
			return nil
		}
		if err != nil {
			if isDeterministicRejection(err) {
				return err
			}
			f.halt(fmt.Errorf("raft index %d: applying append: %w", l.Index, err))
			return err
		}
		return entry

	case cmdSignCheckpoint:
		skipped, err := f.store.SaveCheckpointReplicated(ctx, int64(l.Index), cmd.TreeSize, cmd.RootHash, cmd.NoteText)
		if skipped {
			return nil
		}
		if errors.Is(err, store.ErrCheckpointFork) {
			// Deterministic, but not survivable. Under Raft this is unreachable:
			// committed entries are never lost, so no two leaders can compute
			// different roots at the same size. Reaching it means an assumption
			// this design rests on is false, and continuing would mean publishing
			// a fork under the node's own signing key.
			f.halt(fmt.Errorf("raft index %d: %w", l.Index, err))
			return err
		}
		if err != nil {
			f.halt(fmt.Errorf("raft index %d: saving checkpoint: %w", l.Index, err))
			return err
		}
		return nil

	case cmdWebhook:
		skipped, err := f.store.ApplyWebhookReplicated(ctx, l.Index, *cmd.Webhook)
		if skipped {
			return nil
		}
		if err != nil {
			if isDeterministicRejection(err) {
				return err
			}
			f.halt(fmt.Errorf("raft index %d: applying webhook change: %w", l.Index, err))
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: %q", errUnknownCommand, cmd.Kind)
}

// isDeterministicRejection reports whether every replica would reject this
// command the same way. Only these may be swallowed and returned to the
// client; anything else stops the replica.
func isDeterministicRejection(err error) bool {
	return errors.Is(err, store.ErrInvalidWrite) ||
		errors.Is(err, store.ErrVersionConflict) ||
		errors.Is(err, store.ErrNotFound)
}

// snapshotData is the replicated state, captured whole. Tree hashes are left
// out because they are derivable — Restore recomputes them, which doubles as a
// check that the leaf encoding has not drifted. Checkpoints cannot be
// derived: re-signing would need the identity key, and a restoring replica
// should not need it.
type snapshotData struct {
	Entries     []store.Entry      `json:"entries"`
	Checkpoints []store.Checkpoint `json:"checkpoints"`
	// Subscriptions and their dead letters are replicated state like the rest,
	// so they have to travel in the snapshot. A replica restored without them
	// would come back having forgotten who asked to be notified — the same
	// silent stop that keeping subscriptions local would have caused, just
	// reached by a different route.
	Subscriptions []store.WebhookSubscription `json:"subscriptions,omitempty"`
	DeadLetters   []store.DeadLetter          `json:"dead_letters,omitempty"`
	// AppliedIndex is how far the captured state had consumed the command
	// stream. Without it a restored replica would replay from the beginning and
	// apply everything the snapshot already contains a second time.
	AppliedIndex uint64 `json:"applied_index"`
}

// Snapshot captures the state so old Raft log entries can be discarded. Raft
// calls it on the FSM goroutine with no Apply in flight, so a plain read is a
// consistent point-in-time view.
func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	entries, err := f.store.AllEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot entries: %w", err)
	}
	checkpoints, err := f.store.AllCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot checkpoints: %w", err)
	}
	subs, err := f.store.AllSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot subscriptions: %w", err)
	}
	deads, err := f.store.AllDeadLetters(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot dead letters: %w", err)
	}
	applied, err := f.store.AppliedIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot applied index: %w", err)
	}
	return &snapshot{data: snapshotData{
		Entries: entries, Checkpoints: checkpoints,
		Subscriptions: subs, DeadLetters: deads, AppliedIndex: applied,
	}}, nil
}

// Restore rebuilds this replica from a snapshot, discarding local state. It
// runs when a replica has fallen too far behind for the leader to catch it up
// with individual entries.
func (f *fsm) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var data snapshotData
	if err := json.NewDecoder(rc).Decode(&data); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*applyTimeout)
	defer cancel()
	// Subscriptions first: their cursors point at seqs, and Restore replays the
	// entries those seqs name. Either order works today, but restoring the
	// pointers before the thing they point into keeps it that way.
	if err := f.store.RestoreWebhooks(ctx, data.Subscriptions, data.DeadLetters); err != nil {
		return fmt.Errorf("restore webhooks: %w", err)
	}
	return f.store.Restore(ctx, data.Entries, data.Checkpoints, data.AppliedIndex)
}

type snapshot struct{ data snapshotData }

func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.data); err != nil {
		sink.Cancel()
		return fmt.Errorf("write snapshot: %w", err)
	}
	return sink.Close()
}

func (s *snapshot) Release() {}
