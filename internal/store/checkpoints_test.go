package store

import (
	"context"
	"testing"
	"time"
)

// The two series must arrive on ONE grid, from one query. An earlier version
// bucketed entries in SQL and checkpoints in the browser; the grids were offset
// by however far into a bucket the request arrived, so a write and the
// checkpoint that signed it landed in different cells about a third of the
// time, and a healthy node was painted with the colour reserved for a fault.
func TestHistoryPutsBothSeriesOnOneGrid(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Two entries and a checkpoint over them.
	for i := 0; i < 2; i++ {
		if _, err := s.Append(ctx, AppendInput{EntryType: "namespace",
			Namespace: "ns" + string(rune('a'+i)), PayloadRaw: []byte(`{}`), CreatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveCheckpoint(ctx, 2, make([]byte, 32), "note"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.History(ctx, 6, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("got %d buckets, want exactly the 6 asked for", len(rows))
	}
	// Oldest first, evenly spaced, no gaps: the page indexes them positionally.
	for i := 1; i < len(rows); i++ {
		gap := rows[i].At.Sub(rows[i-1].At)
		if gap != time.Hour {
			t.Fatalf("bucket %d is %v after its predecessor, want 1h — the grid has holes", i, gap)
		}
	}
	var entries, cps int64
	var maxSeq int64 = -1
	for _, b := range rows {
		entries += b.Entries
		cps += b.Checkpoints
		if b.MaxSeq > maxSeq {
			maxSeq = b.MaxSeq
		}
	}
	if entries == 0 {
		t.Error("no entries in the window, so a signing gap could not be attributed")
	}
	if cps == 0 {
		t.Error("no checkpoints in the window")
	}
	// MaxSeq is what makes a fault decidable without comparing clocks.
	if maxSeq < 0 {
		t.Error("no log position reported, so 'unsigned' would have to be guessed from timing")
	}
}

// A silly bucket count must not become an unbounded generate_series.
func TestHistoryIsBounded(t *testing.T) {
	s := testStore(t)
	rows, err := s.History(context.Background(), 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 500 {
		t.Errorf("got %d buckets from an absurd request", len(rows))
	}
}

// A snapshot restore must preserve WHEN each checkpoint was signed. The column
// defaults to now(), so without carrying it a replica stamped every checkpoint
// it had ever received with the instant it joined — collapsing the whole
// signing history into one moment, which the status page then reads as an
// outage that never happened.
func TestSnapshotRestoreKeepsSigningTimes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SaveCheckpoint(ctx, 1, make([]byte, 32), "note-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.AllCheckpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CreatedAt.IsZero() {
		t.Fatalf("AllCheckpoints does not carry the signing time: %+v", got)
	}
	// Rewind it to prove the restore preserves rather than restamps.
	got[0].CreatedAt = got[0].CreatedAt.Add(-72 * time.Hour)
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(ctx, nil, got, 0); err != nil {
		t.Fatal(err)
	}
	back, err := s.AllCheckpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("got %d checkpoints after restore, want 1", len(back))
	}
	if d := back[0].CreatedAt.Sub(got[0].CreatedAt).Abs(); d > time.Second {
		t.Errorf("restore restamped the signing time by %v — the history was rewritten", d)
	}
}

// A snapshot taken before Checkpoint carried CreatedAt decodes with the zero
// time. Writing that verbatim would date every restored checkpoint to year 1,
// so a rolling upgrade would leave /status and the checkpoint-age metric
// reporting a history that never happened.
func TestRestoreOfAPreTimestampSnapshot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := []Checkpoint{{TreeSize: 1, RootHash: make([]byte, 32), NoteText: "note"}} // zero CreatedAt
	if err := s.Restore(ctx, nil, old, 0); err != nil {
		t.Fatal(err)
	}
	back, err := s.AllCheckpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("got %d checkpoints, want 1", len(back))
	}
	if back[0].CreatedAt.Year() < 2000 {
		t.Errorf("restored checkpoint dated %v — a snapshot with no timestamp was written verbatim",
			back[0].CreatedAt)
	}
	if time.Since(back[0].CreatedAt) > time.Minute {
		t.Errorf("restored checkpoint dated %v, want about now", back[0].CreatedAt)
	}
}
