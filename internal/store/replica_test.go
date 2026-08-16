package store

import (
	"testing"
)

// A snapshot that cannot be replayed must leave the replica exactly as it was.
//
// The dangerous failure is not a restore that errors — it is one that errors
// after destroying what was there, leaving a replica serving a truncated log
// while reporting nothing wrong.
func TestAFailedRestoreLeavesThePreviousStateIntact(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	for _, in := range []AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "keep",
			PayloadRaw: []byte(`{"a":1}`), State: "live", CreatedBy: "t"},
	} {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.AllEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A snapshot whose second entry cannot apply: its leaf hash does not match
	// what replaying the command produces, which is exactly the divergence
	// Restore is there to catch.
	bad := []Entry{before[0], {
		Seq: 2, EntryType: "registry", Namespace: "ns", Registry: "reg",
		PayloadRaw: []byte(`{}`), CreatedBy: "t", CreatedAt: before[1].CreatedAt,
		LeafHash: []byte("this is not the leaf hash of that command"),
	}}
	if err := s.Restore(ctx, bad, nil, 99); err == nil {
		t.Fatal("Restore accepted a snapshot that does not replay")
	}

	after, err := s.AllEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed restore left %d entries, had %d before", len(after), len(before))
	}
	for i := range before {
		if after[i].Seq != before[i].Seq || string(after[i].LeafHash) != string(before[i].LeafHash) {
			t.Fatalf("entry %d changed across a failed restore", i)
		}
	}
}

// The successful path still replaces the state whole.
func TestASuccessfulRestoreReplacesTheState(t *testing.T) {
	src := testStore(t)
	ctx := t.Context()
	for _, in := range []AppendInput{
		{EntryType: "namespace", Namespace: "src", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
		{EntryType: "registry", Namespace: "src", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
	} {
		if _, err := src.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := src.AllEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dst := testStore(t)
	if _, err := dst.Append(ctx, AppendInput{
		EntryType: "namespace", Namespace: "stale", PayloadRaw: []byte(`{}`), CreatedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := dst.Restore(ctx, snapshot, nil, 7); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := dst.AllEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(snapshot) {
		t.Fatalf("restored %d entries, snapshot had %d", len(got), len(snapshot))
	}
	for i := range snapshot {
		if got[i].Namespace != snapshot[i].Namespace || string(got[i].LeafHash) != string(snapshot[i].LeafHash) {
			t.Fatalf("entry %d does not match the snapshot", i)
		}
	}
}
