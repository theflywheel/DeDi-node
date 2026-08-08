package webhook

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

type storeProposer struct{ s *store.Store }

func (p storeProposer) Webhook(ctx context.Context, c store.WebhookCommand) error {
	return p.s.ApplyWebhook(ctx, c)
}

// capture is a consumer. It records what it was sent and answers with whatever
// the test tells it to.
type capture struct {
	mu     sync.Mutex
	got    []Notification
	heads  []http.Header
	bodies [][]byte
	status atomic.Int32
	hits   atomic.Int32
}

func (c *capture) server(t *testing.T) *httptest.Server {
	t.Helper()
	c.status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var n Notification
		json.Unmarshal(body, &n)
		c.mu.Lock()
		c.got = append(c.got, n)
		c.heads = append(c.heads, r.Header.Clone())
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
		c.hits.Add(1)
		w.WriteHeader(int(c.status.Load()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *capture) seen() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Notification(nil), c.got...)
}

func fixture(t *testing.T, target string) (*store.Store, *Deliverer, ed25519.PublicKey) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: "beckn", PayloadRaw: []byte(`{}`), CreatedBy: "test"},
		{EntryType: "registry", Namespace: "beckn", Registry: "subscribers", PayloadRaw: []byte(`{}`), CreatedBy: "test"},
	} {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ApplyWebhook(ctx, store.WebhookCommand{
		Op: store.WebhookCreate, ID: "sub-1", Namespace: "beckn", Registry: "subscribers",
		TargetURL: target, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return s, &Deliverer{
		Store: s, Proposer: storeProposer{s}, Origin: "beckn.test/log",
		PublicURL: "https://node.example", Signer: priv,
		// httptest listens on 127.0.0.1, which the SSRF gate refuses by design.
		AllowPrivateTargets: true,
		Client:              &http.Client{Timeout: 2 * time.Second},
	}, pub
}

func publish(t *testing.T, s *store.Store, name, state string) store.Entry {
	t.Helper()
	e, err := s.Append(context.Background(), store.AppendInput{
		EntryType: "record", Namespace: "beckn", Registry: "subscribers",
		RecordName: name, State: state, PayloadRaw: []byte(`{"subscriber_id":"x"}`),
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The point of the whole subsystem: a revocation reaches the consumer without
// waiting for a cache to expire.
func TestARevocationIsDeliveredAndTheCursorAdvances(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, _ := fixture(t, srv.URL+"/hook")
	ctx := context.Background()

	revoked := publish(t, s, "key-1", "revoked")
	d.Sweep(ctx)

	got := c.seen()
	if len(got) != 1 {
		t.Fatalf("want one delivery, got %d", len(got))
	}
	if got[0].Seq != revoked.Seq || got[0].State != "revoked" || got[0].RecordName != "key-1" {
		t.Fatalf("unexpected notification: %+v", got[0])
	}
	// The seq is the load-bearing field: it is what lets the consumer fetch the
	// entry and check its inclusion rather than believe the push.
	if got[0].LookupURL == "" {
		t.Error("no lookup URL — the consumer has to reconstruct our routing to verify anything")
	}

	sub, err := s.Subscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.CursorSeq != revoked.Seq {
		t.Fatalf("cursor at %d, want %d", sub.CursorSeq, revoked.Seq)
	}

	// A second sweep with nothing new must not re-send. A consumer that gets the
	// same revocation every five seconds forever learns to ignore us.
	d.Sweep(ctx)
	if n := len(c.seen()); n != 1 {
		t.Fatalf("a quiet sweep delivered again: %d total", n)
	}
}

// A consumer verifies a push exactly as it verifies a lookup: same preimage,
// same key. If this drifts, every consumer's verification silently starts
// failing — or worse, silently starts passing on something else.
func TestADeliveryIsSignedWithTheNodeIdentityKey(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, pub := fixture(t, srv.URL+"/hook")
	publish(t, s, "key-1", "revoked")
	d.Sweep(context.Background())

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.heads) != 1 {
		t.Fatalf("want one delivery, got %d", len(c.heads))
	}
	h, body := c.heads[0], c.bodies[0]
	if h.Get(publisher.HeaderKeyID) != "beckn.test/log" {
		t.Errorf("key id is %q, want the node origin", h.Get(publisher.HeaderKeyID))
	}
	ts, err := time.Parse(time.RFC3339, h.Get(publisher.HeaderTimestamp))
	if err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(h.Get(publisher.HeaderSignature))
	if err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	pre := publisher.Preimage(http.MethodPost, "/hook", body, publisher.Precondition{}, ts)
	if !ed25519.Verify(pub, pre, sig) {
		t.Fatal("the delivery signature does not verify against the node's key")
	}
	// And it must actually cover the body: a signature over a constant would
	// pass the check above while proving nothing about what was delivered.
	if ed25519.Verify(pub, publisher.Preimage(http.MethodPost, "/hook",
		append(body, ' '), publisher.Precondition{}, ts), sig) {
		t.Fatal("the signature verifies over a different body")
	}
}

// Every replica holds the same subscriptions after they were made replicated.
// If every replica also delivered, a consumer would be told of each revocation
// three times — and would have no way to tell that from three revocations.
func TestOnlyTheLeaderDelivers(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, _ := fixture(t, srv.URL+"/hook")
	leader := false
	d.IsLeader = func() bool { return leader }

	publish(t, s, "key-1", "revoked")
	d.Sweep(context.Background())
	if n := c.hits.Load(); n != 0 {
		t.Fatalf("a follower delivered %d times", n)
	}

	// It is promoted, and picks up exactly where the cluster's cursor was.
	leader = true
	d.Sweep(context.Background())
	if n := c.hits.Load(); n != 1 {
		t.Fatalf("after promotion the new leader delivered %d times, want 1", n)
	}
}

// A failing consumer is retried, not abandoned — but not forever. Holding the
// cursor on a payload nobody is going to fix would queue the next revocation
// behind it, which is the failure this subsystem exists to prevent.
func TestAFailingConsumerIsRetriedThenDeadLettered(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, _ := fixture(t, srv.URL+"/hook")
	d.MaxAttempts = 3
	ctx := context.Background()

	c.status.Store(http.StatusInternalServerError)
	poison := publish(t, s, "key-1", "live")
	next := publish(t, s, "key-2", "revoked")

	for i := 0; i < d.MaxAttempts; i++ {
		d.Sweep(ctx)
		// Backoff would otherwise skip the later attempts within one test run.
		d.mu.Lock()
		if st, ok := d.retry["sub-1"]; ok {
			st.notUntil = time.Now().Add(-time.Second)
			d.retry["sub-1"] = st
		}
		d.mu.Unlock()
	}

	deads, err := s.DeadLetters(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(deads) != 1 || deads[0].Seq != poison.Seq {
		t.Fatalf("want seq %d dead-lettered, got %+v", poison.Seq, deads)
	}
	if deads[0].Attempts != d.MaxAttempts {
		t.Errorf("recorded %d attempts, want %d", deads[0].Attempts, d.MaxAttempts)
	}

	// And the queue is moving again: the consumer recovers and gets the
	// revocation that was stuck behind the poison entry.
	c.status.Store(http.StatusOK)
	d.Sweep(ctx)
	got := c.seen()
	if len(got) == 0 || got[len(got)-1].Seq != next.Seq {
		t.Fatalf("the revocation behind the dead letter was never delivered: %+v", got)
	}
}

// Backoff has to actually hold. Without it a down consumer is hammered every
// sweep, and the attempt counter burns through to a dead letter in seconds
// rather than giving a brief outage time to end.
func TestBackoffHoldsBetweenAttempts(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, _ := fixture(t, srv.URL+"/hook")
	c.status.Store(http.StatusInternalServerError)
	publish(t, s, "key-1", "revoked")

	ctx := context.Background()
	d.Sweep(ctx)
	d.Sweep(ctx)
	d.Sweep(ctx)
	if n := c.hits.Load(); n != 1 {
		t.Fatalf("three immediate sweeps made %d attempts; backoff is not holding", n)
	}
}

// The target is re-checked at delivery, not only at creation: DNS is the target
// operator's to change, and a name that resolved publicly yesterday can point
// at the cloud metadata service today.
func TestATargetThatTurnedPrivateIsRefusedAtDelivery(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	s, d, _ := fixture(t, srv.URL+"/hook")
	d.AllowPrivateTargets = false // the httptest server is on 127.0.0.1
	publish(t, s, "key-1", "revoked")

	d.Sweep(context.Background())
	if n := c.hits.Load(); n != 0 {
		t.Fatalf("delivered to a non-public target %d times", n)
	}
	sub, err := s.Subscription(context.Background(), "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.CursorSeq != 1 {
		t.Fatalf("the cursor advanced past an entry that was never delivered (at %d)", sub.CursorSeq)
	}
}

// A subscription with nothing pending looks identical to one whose loop died
// hours ago. The loop's own health is what tells them apart, so it is reported
// separately rather than inferred from the subscriptions.
func TestStatusReportsTheLoopSeparatelyFromTheSubscriptions(t *testing.T) {
	c := &capture{}
	srv := c.server(t)
	_, d, _ := fixture(t, srv.URL+"/hook")

	if h := d.Status(); !h.LastSweep.IsZero() {
		t.Fatal("a loop that has never run reports a sweep time")
	}
	d.Sweep(context.Background())
	if h := d.Status(); h.LastSweep.IsZero() || !h.Leader {
		t.Fatalf("after a sweep the loop still reports %+v", h)
	}
}
