package crawl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/testdb"
)

// testSigner is this node's own identity for the build in
// TestCrawledDataIsNeverRepublishedAsOurs.
func testSigner(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, "ours-key-1"
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// crawlInto runs a full fetch-and-ingest against a stub publisher, the way the
// daemon loop does, so these tests never hand Ingest a Result that Fetch would
// have refused.
func crawlInto(t *testing.T, s *store.Store, p *publisher) Ingested {
	t.Helper()
	res, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := (&Ingester{Store: s, Writer: s}).Ingest(context.Background(), res)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return got
}

// TestIngestExposesRecordsAtTheSpecTriple is §13's third DeDi-server condition:
// a crawled record must answer at {namespace}/{registry}/{record}, the same way
// a locally published one does, so a reader asks one question regardless of who
// authored the answer.
func TestIngestExposesRecordsAtTheSpecTriple(t *testing.T) {
	s := testStore(t)
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	got := crawlInto(t, s, p)
	if got.Namespaces != 1 || got.Registries != 1 || got.Records != 1 {
		t.Fatalf("ingested %s", got)
	}
	e, err := s.Resolve(context.Background(), "record", "partner.example", "keys", "auth", nil, nil)
	if err != nil {
		t.Fatalf("crawled record does not resolve at its triple: %v", err)
	}
	if e.State != "live" {
		t.Fatalf("state = %q", e.State)
	}
	// Authorship names the source, so provenance is answerable from the log
	// alone rather than from operator memory.
	if e.CreatedBy != "crawler:"+p.origin() {
		t.Fatalf("created_by = %q", e.CreatedBy)
	}
}

// TestIngestIsIdempotent: a crawl loop runs on a cadence against publishers that
// mostly do not change. If an unchanged crawl still appended, the log would grow
// at the crawl rate forever and every checkpoint would advance over data nobody
// touched.
func TestIngestIsIdempotent(t *testing.T) {
	s := testStore(t)
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	crawlInto(t, s, p)
	before, err := s.TreeSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second := crawlInto(t, s, p)
	if second.Changed() {
		t.Fatalf("an unchanged re-crawl wrote %s", second)
	}
	after, err := s.TreeSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("log grew from %d to %d on an unchanged re-crawl", before, after)
	}
}

// TestIngestWithdrawsRecordsThatLeftTheFile: a record removed upstream must stop
// resolving here. §5.1 puts record lifecycle in a negative list rather than in
// absence, and this node already publishes revocations that way, so a
// disappearance is mirrored as a revocation with its history intact.
func TestIngestWithdrawsRecordsThatLeftTheFile(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
		{RecordName: "gateway", Details: json.RawMessage(`{"publicKey":"def"}`)},
	}, testNextUpdate)
	crawlInto(t, s, p)

	// Upstream drops one.
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)
	got := crawlInto(t, s, p)
	if got.Revoked != 1 {
		t.Fatalf("withdrawal not mirrored: %s", got)
	}
	e, err := s.Resolve(ctx, "record", "partner.example", "keys", "gateway", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.State != "revoked" {
		t.Fatalf("state = %q, want revoked", e.State)
	}
	// The earlier version is still there: withdrawal is an append, not an erasure.
	versions, err := s.Versions(ctx, "record", "partner.example", "keys", "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
}

// TestIngestRefusesToOverwriteOurOwnNamespace is the containment property. A
// crawl is data from a party we do not control; if it could write into a
// namespace this node publishes, any domain we crawl could overwrite our own
// directory just by claiming the same namespace name.
func TestIngestRefusesToOverwriteOurOwnNamespace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: "partner.example",
		PayloadRaw: []byte(`{"description":"ours"}`), CreatedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"attacker"}`)},
	}, testNextUpdate)

	res, err := fetcher().Fetch(ctx, "http://"+p.origin(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Ingester{Store: s, Writer: s}).Ingest(ctx, res)
	if !errors.Is(err, ErrNamespaceOwned) {
		t.Fatalf("want ErrNamespaceOwned, got %v", err)
	}
	// Nothing of theirs landed.
	if _, err := s.Resolve(ctx, "record", "partner.example", "keys", "auth", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused crawl still wrote a record: %v", err)
	}
}

// TestIngestRefusesTheReservedPrefix: `_` namespaces are this node's own
// bookkeeping and are hidden from every read surface. A remote publisher able to
// write there could park records where nobody would ever look at them.
func TestIngestRefusesTheReservedPrefix(t *testing.T) {
	s := testStore(t)
	p := newPublisher(t, "")
	p.publish("_witness", "keys", nil, testNextUpdate)
	res, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Ingester{Store: s, Writer: s}).Ingest(context.Background(), res); err == nil {
		t.Fatal("a crawl wrote into the reserved _ prefix")
	}
}

// TestCrawledDataIsNeverRepublishedAsOurs is the laundering guard. We serve
// other publishers' records, but we must not re-sign them under our key: a
// crawler downstream would then attribute them to us, and the original
// publisher's signature would stop being the thing that vouches for them.
func TestCrawledDataIsNeverRepublishedAsOurs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := newPublisher(t, "")
	p.publish("partner.example", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)
	crawlInto(t, s, p)

	// Something of our own, so the build has a reason to produce output at all.
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: "ours.example",
		PayloadRaw: []byte(`{"description":"ours"}`), CreatedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: "ours.example",
		Registry: "keys", PayloadRaw: []byte(`{"schema":{"type":"object"}}`), CreatedBy: "seed"}); err != nil {
		t.Fatal(err)
	}

	signer, keyID := testSigner(t)
	_, files, err := dedifile.Build(ctx, s, dedifile.Config{
		Domain: "ours.example", BaseURL: "https://ours.example",
		Signer: signer, Kid: keyID, Now: time.Now(), Freshness: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Namespace == "partner.example" {
			t.Fatalf("we re-published %s/%s, which we crawled from someone else",
				f.Namespace, f.Registry.Name)
		}
	}
	if len(files) == 0 {
		t.Fatal("build produced nothing, so this test proved nothing")
	}
}
