package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

func mustAppend(t *testing.T, s *Store, in AppendInput) Entry {
	t.Helper()
	e, err := s.Append(context.Background(), in)
	if err != nil {
		t.Fatalf("append %+v: %v", in, err)
	}
	return e
}

func seedNSReg(t *testing.T, s *Store) {
	t.Helper()
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{"description":"test ns"}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{"description":"test reg"}`), CreatedBy: "t"})
}

func TestAppendAssignsDenseSeqAndVersions(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	e1 := mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r1", PayloadRaw: []byte(`{"a": 1}`), CreatedBy: "t"})
	e2 := mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r1", PayloadRaw: []byte(`{"a": 2}`), CreatedBy: "t"})
	if e1.Seq != 2 || e2.Seq != 3 {
		t.Fatalf("want seq 2,3 got %d,%d", e1.Seq, e2.Seq)
	}
	if e1.VersionNum != 1 || e2.VersionNum != 2 {
		t.Fatalf("want version_num 1,2 got %d,%d", e1.VersionNum, e2.VersionNum)
	}
	// payload compacted, digest over compacted bytes
	if !bytes.Equal(e1.PayloadRaw, []byte(`{"a":1}`)) {
		t.Fatalf("payload not compacted: %q", e1.PayloadRaw)
	}
	d := sha256.Sum256(e1.PayloadRaw)
	if !bytes.Equal(e1.Digest, d[:]) {
		t.Fatal("digest is not sha256(compacted payload)")
	}
	if e1.State != "live" {
		t.Fatalf("default record state: got %q want live", e1.State)
	}
}

func TestAppendRejectsOrphans(t *testing.T) {
	s := testStore(t)
	_, err := s.Append(context.Background(), AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	if err == nil {
		t.Fatal("record without registry accepted")
	}
	_, err = s.Append(context.Background(), AppendInput{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	if err == nil {
		t.Fatal("registry without namespace accepted")
	}
}

// The precondition must be evaluated inside the append transaction, because it
// is what makes a captured signed write unreplayable: a replay carries the
// digest of a version that has since been superseded.
func TestAppendPrecondition(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	rec := AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "r1", CreatedBy: "t"}

	create := rec
	create.PayloadRaw, create.ExpectedAbsent = []byte(`{"a":1}`), true
	e1 := mustAppend(t, s, create)
	if e1.VersionNum != 1 {
		t.Fatalf("version_num = %d, want 1", e1.VersionNum)
	}

	// Creating again must fail: something is already there.
	if _, err := s.Append(context.Background(), create); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("second create: err = %v, want ErrVersionConflict", err)
	}

	update := rec
	update.PayloadRaw, update.ExpectedPrevDigest, update.ExpectedPrevState = []byte(`{"a":2}`), e1.Digest, e1.State
	e2 := mustAppend(t, s, update)
	if e2.VersionNum != 2 {
		t.Fatalf("version_num = %d, want 2", e2.VersionNum)
	}

	// Replaying the update now that v2 is current must be refused, not applied
	// on top — otherwise a replay silently reverts the record to {"a":1}.
	if _, err := s.Append(context.Background(), update); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("replayed update: err = %v, want ErrVersionConflict", err)
	}

	// And an update whose expected version never existed is a conflict too.
	orphan := rec
	orphan.RecordName, orphan.PayloadRaw = "r2", []byte(`{"a":3}`)
	orphan.ExpectedPrevDigest, orphan.ExpectedPrevState = e1.Digest, e1.State
	if _, err := s.Append(context.Background(), orphan); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("update to absent record: err = %v, want ErrVersionConflict", err)
	}

	revoke := rec
	revoke.PayloadRaw, revoke.State = e2.PayloadRaw, "revoked"
	revoke.ExpectedPrevDigest, revoke.ExpectedPrevState = e2.Digest, e2.State
	e3 := mustAppend(t, s, revoke)

	replay := rec
	replay.PayloadRaw = e2.PayloadRaw
	replay.ExpectedPrevDigest, replay.ExpectedPrevState = e3.Digest, "live"
	if _, err := s.Append(context.Background(), replay); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("same digest with stale state: err = %v, want ErrVersionConflict", err)
	}
}

func TestAppendConcurrentStaysDense(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	const workers, per = 10, 5
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				name := fmt.Sprintf("rec-%d", w)
				if _, err := s.Append(context.Background(), AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: name, PayloadRaw: []byte(`{"i":1}`), CreatedBy: "t"}); err != nil {
					t.Errorf("append: %v", err)
				}
			}
		}(w)
	}
	wg.Wait()
	ctx := context.Background()
	var count, maxSeq int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*), max(seq) FROM log_entries`).Scan(&count, &maxSeq); err != nil {
		t.Fatal(err)
	}
	want := int64(2 + workers*per)
	if count != want || maxSeq != want-1 {
		t.Fatalf("gaps: count=%d maxSeq=%d want count=%d maxSeq=%d", count, maxSeq, want, want-1)
	}
	// per-resource version_nums contiguous 1..per
	for w := 0; w < workers; w++ {
		var vmax int32
		if err := s.pool.QueryRow(ctx, `SELECT max(version_num) FROM log_entries WHERE record_name=$1`, fmt.Sprintf("rec-%d", w)).Scan(&vmax); err != nil {
			t.Fatal(err)
		}
		if vmax != per {
			t.Fatalf("rec-%d max version_num=%d want %d", w, vmax, per)
		}
	}
}

// refMTH is an independent RFC 6962 Merkle Tree Head implementation used to
// cross-check the incrementally maintained tree.
func refMTH(leaves [][]byte) [32]byte {
	n := len(leaves)
	if n == 1 {
		return sha256.Sum256(append([]byte{0x00}, leaves[0]...))
	}
	k := 1
	for k*2 < n {
		k *= 2
	}
	l := refMTH(leaves[:k])
	r := refMTH(leaves[k:])
	return sha256.Sum256(append(append([]byte{0x01}, l[:]...), r[:]...))
}

func TestTreeRootMatchesReferenceImplementation(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	var leaves [][]byte
	collect := func(e Entry) {
		leaves = append(leaves, merkle.LeafBytes(e.EntryType, e.Namespace, e.Registry, e.RecordName, e.VersionNum, e.Digest, e.CreatedBy, e.CreatedAt))
	}
	// rebuild the two seed leaves from the DB
	for seq := int64(0); seq < 2; seq++ {
		var e Entry
		err := s.pool.QueryRow(context.Background(),
			`SELECT seq, entry_type, namespace, registry, record_name, version_num, payload_raw, digest, state, created_by, created_at, leaf_hash
			 FROM log_entries WHERE seq=$1`, seq).
			Scan(&e.Seq, &e.EntryType, &e.Namespace, &e.Registry, &e.RecordName, &e.VersionNum, &e.PayloadRaw, &e.Digest, &e.State, &e.CreatedBy, &e.CreatedAt, &e.LeafHash)
		if err != nil {
			t.Fatal(err)
		}
		collect(e)
	}
	for i := 0; i < 7; i++ {
		e := mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: fmt.Sprintf("r%d", i), PayloadRaw: []byte(fmt.Sprintf(`{"i":%d}`, i)), CreatedBy: "t"})
		collect(e)
		size, err := s.TreeSize(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if size != int64(len(leaves)) {
			t.Fatalf("tree size %d want %d", size, len(leaves))
		}
		root, err := s.TreeRoot(context.Background(), size)
		if err != nil {
			t.Fatal(err)
		}
		want := refMTH(leaves)
		if root != tlog.Hash(want) {
			t.Fatalf("root mismatch at size %d", size)
		}
	}
}
