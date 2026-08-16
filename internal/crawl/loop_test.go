package crawl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func testLoop(s *store.Store, domains ...string) *Loop {
	return &Loop{
		Store: s, Writer: s, Fetcher: fetcher(), Domains: domains,
		Now: func() time.Time { return testNextUpdate.Add(-time.Hour) },
	}
}

// TestLoopPinsOnFirstUseAndAlarmsOnAChange is the trust model in one test.
// Trust on first use proves nothing by itself — an attacker present at that
// moment gets pinned instead — but from then on a silent key change is caught,
// which is the monitor §14 leaves as an open question.
func TestLoopPinsOnFirstUseAndAlarmsOnAChange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	l := testLoop(s, "http://"+p.origin())
	l.Once(ctx)

	st, ok := l.state(ctx, "http://"+p.origin())
	if !ok || st.PinnedKey == "" {
		t.Fatalf("first crawl did not pin a key: %+v", st)
	}
	if st.LastError != "" {
		t.Fatalf("first crawl errored: %s", st.LastError)
	}
	pinned := st.PinnedKey

	// The publisher rotates — or is taken over; from here they are the same
	// thing, which is the point.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	p.priv = priv
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"attacker"}`)},
	}, testNextUpdate)
	l.Once(ctx)

	st, _ = l.state(ctx, "http://"+p.origin())
	if st.PinnedKey != pinned {
		t.Fatal("the pin moved on its own; a takeover would be silently accepted")
	}
	if st.LastError == "" {
		t.Fatal("a key change was not recorded")
	}
	// Surfaced as a revoked source, so it reads as broken wherever state is
	// already rendered rather than needing its own alert path.
	e, err := s.Resolve(ctx, "record", crawlNS, crawlRegistry, "http://"+p.origin(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.State != "revoked" {
		t.Fatalf("source state = %q, want revoked", e.State)
	}
	// And the attacker's record never landed.
	rec, err := s.Resolve(ctx, "record", "partner.example", "keys", "auth", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.PayloadRaw) == `{"publicKey":"attacker"}` {
		t.Fatal("data signed by the changed key was ingested")
	}
}

// TestLoopDoesNotGrowTheLogWhenNothingChanges covers the bookkeeping entry
// itself, not just the ingested data: a per-domain state record rewritten on
// every tick would reintroduce exactly the unbounded growth Ingest avoids.
func TestLoopDoesNotGrowTheLogWhenNothingChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	l := testLoop(s, "http://"+p.origin())
	l.Once(ctx)
	before, err := s.TreeSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		l.Once(ctx)
	}
	after, err := s.TreeSize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("three no-op crawls grew the log from %d to %d", before, after)
	}
}

// TestLoopOnlyCrawlsOnTheLeader: in a cluster, three replicas each fetching the
// same directory would each propose the same writes, and the two that lost
// would have spent a fetch learning what the log already knew.
func TestLoopOnlyCrawlsOnTheLeader(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", nil, testNextUpdate)

	l := testLoop(s, "http://"+p.origin())
	l.IsWriter = func() bool { return false }
	l.Once(ctx)

	if size, err := s.TreeSize(ctx); err != nil || size != 0 {
		t.Fatalf("a follower crawled: tree size %d (err %v)", size, err)
	}
}

// TestLoopKeepsGoingWhenOneSourceFails: a partner whose certificate expired
// must not take our whole view of the network offline.
func TestLoopKeepsGoingWhenOneSourceFails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	good := newPublisher(t, "")
	good.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	l := testLoop(s, "http://127.0.0.1:1", "http://"+good.origin())
	l.Once(ctx)

	if _, err := s.Resolve(ctx, "record", "partner.example", "keys", "auth", nil, nil); err != nil {
		t.Fatalf("a healthy source was skipped because an earlier one failed: %v", err)
	}
}
