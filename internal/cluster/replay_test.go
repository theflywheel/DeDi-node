package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Raft keeps its applied index in memory and, on restart, replays every
// committed entry into the state machine — correct when the state machine is
// rebuilt from a snapshot, wrong when it lives in Postgres and survives the
// process. Without a persisted applied index a restarting replica applies
// everything a second time, ends up with more entries than its peers, and
// computes a different root under the same identity key.
//
// This is easy to miss, because a *publisher* write carries a precondition and
// a replay is rejected by it. Witness verdicts carry no precondition, so they
// are what these tests use: the replay would succeed and simply append another
// version.

func TestReplayingTheCommandStreamDoesNotDuplicateEntries(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	ctx := context.Background()

	seed(t, leader)
	// Deliberately precondition-free, the way the witness records a verdict.
	for i := 0; i < 3; i++ {
		if _, err := leader.node.Append(ctx, store.AppendInput{
			EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "verdict",
			PayloadRaw: []byte(fmt.Sprintf(`{"size":%d}`, i)), CreatedBy: "witness",
		}); err != nil {
			t.Fatalf("verdict %d: %v", i, err)
		}
	}
	want := waitForRoot(t, rs, 5)

	// Re-apply the whole committed stream, exactly as Raft does on restart. If
	// the applied index were not persisted, each of these would append again.
	applied, err := leader.store.AppliedIndex(ctx)
	if err != nil {
		t.Fatalf("applied index: %v", err)
	}
	if applied == 0 {
		t.Fatal("no applied index was persisted — a restart would replay everything")
	}
	for idx := uint64(1); idx <= applied; idx++ {
		_, skipped, err := leader.store.ApplyReplicated(ctx, idx, store.AppendInput{
			EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "verdict",
			PayloadRaw: []byte(`{"replayed":true}`), CreatedBy: "witness",
			CreatedAt: time.Unix(0, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("replaying index %d: %v", idx, err)
		}
		if !skipped {
			t.Fatalf("index %d was applied a second time — this replica has now forked", idx)
		}
	}

	size, err := leader.store.TreeSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Fatalf("tree grew to %d during replay, want 5", size)
	}
	root, err := leader.store.TreeRoot(ctx, size)
	if err != nil {
		t.Fatal(err)
	}
	if root.String() != want {
		t.Fatalf("root changed during replay:\n  before %s\n  after  %s", want, root.String())
	}
}

func TestANewIndexStillAppliesAfterASkippedReplay(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	ctx := context.Background()
	seed(t, leader)
	waitForRoot(t, rs, 2)

	applied, err := leader.store.AppliedIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Skipping must not wedge the replica: the very next index has to apply.
	// A guard that refused everything would look identical in the test above.
	_, skipped, err := leader.store.ApplyReplicated(ctx, applied+1, store.AppendInput{
		EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "fresh",
		PayloadRaw: []byte(`{"a":1}`), CreatedBy: "t", CreatedAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("fresh index: %v", err)
	}
	if skipped {
		t.Fatal("a new index was skipped — the replica would never advance again")
	}
	size, err := leader.store.TreeSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if size != 3 {
		t.Fatalf("size %d, want 3", size)
	}
}
