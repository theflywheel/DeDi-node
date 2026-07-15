package store

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

func TestInclusionProofVerifies(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	var entries []Entry
	for i := 0; i < 6; i++ {
		entries = append(entries, mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: fmt.Sprintf("r%d", i), PayloadRaw: []byte(`{}`), CreatedBy: "t"}))
	}
	ctx := context.Background()
	size, err := s.TreeSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.TreeRoot(ctx, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p, err := s.ProveInclusion(ctx, size, e.Seq)
		if err != nil {
			t.Fatalf("prove seq %d: %v", e.Seq, err)
		}
		leaf := tlog.RecordHash(merkle.LeafBytes(e.EntryType, e.Namespace, e.Registry, e.RecordName, e.VersionNum, e.Digest, e.CreatedBy, e.CreatedAt))
		if err := tlog.CheckRecord(p, size, root, e.Seq, leaf); err != nil {
			t.Fatalf("check seq %d: %v", e.Seq, err)
		}
	}
}

func TestConsistencyProofVerifies(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	ctx := context.Background()
	root2, err := s.TreeRoot(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: fmt.Sprintf("r%d", i), PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	}
	root7, err := s.TreeRoot(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ProveConsistency(ctx, 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := tlog.CheckTree(p, 7, root7, 2, root2); err != nil {
		t.Fatalf("consistency check: %v", err)
	}
}
