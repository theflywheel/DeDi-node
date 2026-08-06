package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Replication rests entirely on Apply being a deterministic state transition:
// every replica runs the same ordered commands against its own database and
// must arrive at the same root. If it does not, the divergence is invisible
// until some later consistency proof fails — long after the entry that caused
// it, and with no obvious way back.
//
// These tests replay a command stream and compare roots. They are the guard on
// that property, and they are why AppendInput carries CreatedAt rather than
// letting each replica read its own clock.

func commandStream(at time.Time) []AppendInput {
	return []AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{"description":"d"}`),
			CreatedBy: "t", CreatedAt: at},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{"description":"d"}`),
			CreatedBy: "t", CreatedAt: at.Add(time.Second)},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r1",
			PayloadRaw: []byte(`{"a":1}`), CreatedBy: "alice", CreatedAt: at.Add(2 * time.Second)},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r1",
			PayloadRaw: []byte(`{"a":2}`), CreatedBy: "bob", CreatedAt: at.Add(3 * time.Second)},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r2",
			PayloadRaw: []byte(`{"b":[1,2,3]}`), CreatedBy: "alice", CreatedAt: at.Add(4 * time.Second)},
	}
}

func replay(t *testing.T, s *Store, cmds []AppendInput) (int64, string) {
	t.Helper()
	ctx := context.Background()
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for i, in := range cmds {
		if _, err := s.Apply(ctx, in); err != nil {
			t.Fatalf("apply command %d: %v", i, err)
		}
	}
	size, err := s.TreeSize(ctx)
	if err != nil {
		t.Fatalf("tree size: %v", err)
	}
	root, err := s.TreeRoot(ctx, size)
	if err != nil {
		t.Fatalf("tree root: %v", err)
	}
	return size, root.String()
}

func TestApplyIsDeterministicAcrossReplays(t *testing.T) {
	s := testStore(t)
	// A fixed instant, not time.Now(): the whole point is that the timestamp
	// comes from the command. Deliberately sub-microsecond so the normalisation
	// inside Apply is exercised rather than assumed.
	at := time.Date(2026, 8, 6, 12, 0, 0, 123456789, time.UTC)
	cmds := commandStream(at)

	size1, root1 := replay(t, s, cmds)
	size2, root2 := replay(t, s, cmds)

	if size1 != size2 || root1 != root2 {
		t.Fatalf("replaying the same commands gave a different log:\n  first:  size %d root %s\n  second: size %d root %s",
			size1, root1, size2, root2)
	}
	if size1 != int64(len(cmds)) {
		t.Fatalf("size = %d, want %d", size1, len(cmds))
	}
}

func TestApplyRejectsAnUnstampedCommand(t *testing.T) {
	s := testStore(t)
	// A replica that accepted this would stamp its own clock and silently fork
	// the tree, so it must be refused at the door rather than defaulted.
	_, err := s.Apply(context.Background(), AppendInput{
		EntryType: "namespace", Namespace: "ns",
		PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t",
	})
	if err == nil {
		t.Fatal("Apply accepted a command with no CreatedAt")
	}
	if !errors.Is(err, ErrInvalidWrite) {
		t.Fatalf("want ErrInvalidWrite, got %v", err)
	}
}

func TestAppendStillStampsForAnUnreplicatedNode(t *testing.T) {
	s := testStore(t)
	// The single-node path must keep working without callers learning about
	// timestamps; only the replicated path stamps deliberately.
	before := time.Now().Add(-time.Second)
	e, err := s.Append(context.Background(), AppendInput{
		EntryType: "namespace", Namespace: "ns",
		PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t",
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if e.CreatedAt.Before(before) || e.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("created_at %v is not a plausible stamp of now", e.CreatedAt)
	}
}
