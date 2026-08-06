package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// The anti-double-sign fence. Two signed checkpoints at the same tree size with
// different roots is a fork under the node's own identity key — indistinguishable
// from the operator tampering that the witness ring exists to catch, and
// unrecoverable once published. Under Raft it should be unreachable; this is
// what happens if it is reached anyway.

func TestSigningTheSameCheckpointTwiceIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root := bytes.Repeat([]byte{0xab}, 32)

	if err := s.SaveCheckpoint(ctx, 7, root, "note"); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Re-publishing an unchanged checkpoint is normal — the checkpointer does it
	// on every tick while the log is quiet.
	if err := s.SaveCheckpoint(ctx, 7, root, "note"); err != nil {
		t.Fatalf("second save of the same checkpoint: %v", err)
	}
}

func TestSigningADifferentRootAtTheSameSizeIsRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := bytes.Repeat([]byte{0xab}, 32)
	forked := bytes.Repeat([]byte{0xcd}, 32)

	if err := s.SaveCheckpoint(ctx, 7, first, "note"); err != nil {
		t.Fatalf("first save: %v", err)
	}
	err := s.SaveCheckpoint(ctx, 7, forked, "other note")
	if !errors.Is(err, ErrCheckpointFork) {
		t.Fatalf("a fork was accepted or misreported: %v", err)
	}

	// And the original must survive intact: the fence rejects the new root, it
	// does not overwrite history with it.
	size, text, err := s.LatestCheckpoint(ctx)
	if err != nil || size != 7 || text != "note" {
		t.Fatalf("stored checkpoint changed: size %d text %q err %v", size, text, err)
	}
}
