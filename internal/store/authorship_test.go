package store

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

func hashOf(leafBytes []byte) []byte {
	h := tlog.RecordHash(leafBytes)
	return h[:]
}

// Authorship is recorded in created_by rather than a side column precisely
// because created_by is part of the leaf preimage: changing who is credited
// changes the leaf hash, so the audit trail is covered by inclusion proofs.
// A separate column would be mutable without invalidating any proof.
func TestAuthorshipIsCoveredByTheLeafHash(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	digest := []byte{1, 2, 3}
	a := merkle.LeafBytes("record", "ns", "reg", "rec", 1, digest, "publisher:op-1", at)
	b := merkle.LeafBytes("record", "ns", "reg", "rec", 1, digest, "publisher:op-2", at)
	if string(a) == string(b) {
		t.Fatal("created_by does not affect the leaf preimage — authorship would not be provable")
	}
	if !contains(string(a), "publisher:op-1") {
		t.Fatalf("leaf does not carry the author: %s", a)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// An entry with no author is unattributable, so it is refused outright.
func TestAppendRequiresAnAuthor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, by := range []string{"", "   "} {
		_, err := s.Append(ctx, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: by})
		if !errors.Is(err, ErrInvalidWrite) {
			t.Fatalf("CreatedBy %q: err = %v, want ErrInvalidWrite", by, err)
		}
	}
}

// The author recorded on a write is what the log returns and what the leaf
// commits to.
func TestAuthorRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "publisher:op-7"})
	e, err := s.Resolve(ctx, "namespace", "ns", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.CreatedBy != "publisher:op-7" {
		t.Fatalf("created_by = %q", e.CreatedBy)
	}
	// The stored leaf must equal a leaf recomputed from the entry, author included.
	want := merkle.LeafBytes(e.EntryType, e.Namespace, e.Registry, e.RecordName, e.VersionNum, e.Digest, e.CreatedBy, e.CreatedAt)
	if hex.EncodeToString(hashOf(want)) != hex.EncodeToString(e.LeafHash) {
		t.Fatal("stored leaf hash does not match a leaf recomputed with the recorded author")
	}
}
