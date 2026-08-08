// Package webhook pushes log changes to the consumers that asked for them.
//
// It exists because revocation otherwise propagates only by cache expiry.
// Measured end to end against two live beckn-onix adapters with ttl 20, a
// revoked key kept validating real traffic for fifteen seconds. A TTL *bounds*
// that window; it cannot remove it, and lowering it trades the exposure for
// lookup load on every validator.
//
// What is delivered is deliberately thin: which entry changed, and where to
// read it. Not the payload. A push is a hint to re-read, never a fact to act on
// — the consumer already has a verifiable way to learn the truth, and a
// subsystem that let a POST substitute for a lookup would have added a way to
// lie about the directory that the directory's whole design excludes. That the
// delivery is signed does not change this. The signature says the push really
// came from this node; only the log says what the node published.
package webhook

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// Proposer applies a change to the subscription table. In a cluster this is the
// Raft proposer; standalone it is the store.
type Proposer interface {
	Webhook(ctx context.Context, c store.WebhookCommand) error
}

// Notification is the delivered body.
//
// Seq is the point of it. With the sequence number the consumer can fetch the
// entry, check its inclusion proof against a signed checkpoint, and satisfy
// itself — so the push accelerates the consumer's own verification instead of
// asking to be trusted in place of it.
type Notification struct {
	Origin     string `json:"origin"`
	Seq        int64  `json:"seq"`
	Namespace  string `json:"namespace"`
	Registry   string `json:"registry"`
	RecordName string `json:"record_name"`
	Version    int32  `json:"version_num"`
	State      string `json:"state"`
	Digest     string `json:"digest"`
	CreatedAt  string `json:"created_at"`
	// LookupURL is where to go and check. Included because the consumer should
	// not have to reconstruct this node's routing from a hostname.
	LookupURL string `json:"lookup_url"`
}

// Deliverer runs the delivery loop on the leader.
type Deliverer struct {
	Store    *store.Store
	Proposer Proposer

	// Origin identifies this node in the notification and as the signing key id.
	Origin string
	// PublicURL is the base a consumer should read back from.
	PublicURL string
	// Signer is the node's identity key. The same key that signs checkpoints, so
	// a consumer that can already verify this node's log can verify its pushes
	// with the key it holds — no second credential to distribute or rotate.
	Signer ed25519.PrivateKey

	// IsLeader gates delivery. Every replica holds the same subscriptions, and
	// enqueueing is replicated; if every replica also delivered, a consumer
	// would see each revocation three times. nil means unreplicated, which is a
	// cluster of one and always the leader.
	IsLeader func() bool

	// AllowPrivateTargets mirrors the API's switch. Re-checked here and not only
	// at creation: a name that resolves publicly today can resolve to
	// 169.254.169.254 tomorrow, and that is the target operator's decision, not
	// this node's.
	AllowPrivateTargets bool

	Interval    time.Duration // between sweeps; default 5s
	MaxAttempts int           // before a seq is dead-lettered; default 6
	Client      *http.Client

	mu        sync.Mutex
	retry     map[string]retryState // by subscription id
	lastSweep time.Time
}

// retryState is deliberately not replicated. It is a fact about the world right
// now — this consumer is down, we last tried at this moment — not a fact about
// the directory, and network.Monitor keeps its observations in memory for the
// same reason. The durable half, how far a subscription has been delivered,
// goes through Raft.
type retryState struct {
	attempts int
	notUntil time.Time
	seq      int64 // which seq the attempts are against
	lastErr  string
}

func (d *Deliverer) interval() time.Duration {
	if d.Interval > 0 {
		return d.Interval
	}
	return 5 * time.Second
}

func (d *Deliverer) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return 6
}

func (d *Deliverer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	// Short. A consumer that cannot answer in this long is already failing to
	// act on the notification, and the retry will find it if it recovers.
	return &http.Client{Timeout: 10 * time.Second}
}

func (d *Deliverer) leader() bool { return d.IsLeader == nil || d.IsLeader() }

func (d *Deliverer) markSwept() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastSweep = time.Now().UTC()
}

// Run sweeps until ctx is cancelled.
func (d *Deliverer) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Sweep(ctx)
		}
	}
}

// Sweep delivers one pending entry per subscription. Exported so a test can
// drive it without waiting on a ticker.
func (d *Deliverer) Sweep(ctx context.Context) {
	if !d.leader() {
		return
	}
	d.markSwept()
	subs, err := d.Store.ActiveSubscriptions(ctx)
	if err != nil {
		log.Printf("webhook: list subscriptions: %v", err)
		return
	}
	for _, sub := range subs {
		if err := d.deliverNext(ctx, sub); err != nil {
			log.Printf("webhook: %s: %v", sub.ID, err)
		}
	}
}

func (d *Deliverer) deliverNext(ctx context.Context, sub store.WebhookSubscription) error {
	pending, err := d.Store.PendingFor(ctx, sub, 1)
	if err != nil || len(pending) == 0 {
		return err
	}
	entry := pending[0]

	if !d.due(sub.ID, entry.Seq) {
		return nil
	}

	sendErr := d.post(ctx, sub, entry)
	if sendErr == nil {
		d.clearRetry(sub.ID)
		return d.Proposer.Webhook(ctx, store.WebhookCommand{
			Op: store.WebhookAdvance, ID: sub.ID, Seq: entry.Seq, At: time.Now().UTC(),
		})
	}

	attempts := d.failed(sub.ID, entry.Seq, sendErr)
	if attempts < d.maxAttempts() {
		return fmt.Errorf("seq %d attempt %d: %w", entry.Seq, attempts, sendErr)
	}
	// Give up on this one and move on. Holding the cursor here would queue the
	// next revocation — the thing this subsystem exists to deliver — behind a
	// payload nobody is going to fix.
	d.clearRetry(sub.ID)
	if err := d.Proposer.Webhook(ctx, store.WebhookCommand{
		Op: store.WebhookDeadLetter, ID: sub.ID, Seq: entry.Seq,
		Attempts: attempts, LastError: sendErr.Error(), At: time.Now().UTC(),
	}); err != nil {
		return err
	}
	return fmt.Errorf("seq %d dead-lettered after %d attempts: %w", entry.Seq, attempts, sendErr)
}

func (d *Deliverer) post(ctx context.Context, sub store.WebhookSubscription, e store.Entry) error {
	target, err := d.checkTarget(ctx, sub.TargetURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(Notification{
		Origin: d.Origin, Seq: e.Seq, Namespace: e.Namespace, Registry: e.Registry,
		RecordName: e.RecordName, Version: e.VersionNum, State: e.State,
		Digest: hex.EncodeToString(e.Digest), CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
		LookupURL: d.lookupURL(e),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(publisher.HeaderKeyID, d.Origin)
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	// The same preimage a publisher signs, so a consumer verifies a push with
	// the rules and the key it already has for verifying this node — rather
	// than a second signing scheme invented for this one path.
	req.Header.Set(publisher.HeaderSignature,
		publisher.Sign(d.Signer, http.MethodPost, target.RequestURI(), body, publisher.Precondition{}, now))

	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("consumer answered %d", resp.StatusCode)
	}
	return nil
}

func (d *Deliverer) lookupURL(e store.Entry) string {
	if d.PublicURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/dedi/lookup/%s/%s/%s", d.PublicURL, e.Namespace, e.Registry, e.RecordName)
}

// due reports whether this subscription's next attempt is allowed yet, and
// resets the counter when the pending seq has moved on.
func (d *Deliverer) due(id string, seq int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.retry[id]
	if !ok || st.seq != seq {
		return true
	}
	return !time.Now().Before(st.notUntil)
}

func (d *Deliverer) failed(id string, seq int64, err error) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retry == nil {
		d.retry = map[string]retryState{}
	}
	st := d.retry[id]
	if st.seq != seq {
		st = retryState{seq: seq}
	}
	st.attempts++
	st.lastErr = err.Error()
	st.notUntil = time.Now().Add(backoff(st.attempts))
	d.retry[id] = st
	return st.attempts
}

func (d *Deliverer) clearRetry(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.retry, id)
}

// backoff grows exponentially and stops at a minute. A consumer that has been
// down for an hour is not helped by being asked every second, and the cap keeps
// recovery prompt without a thundering retry.
func backoff(attempt int) time.Duration {
	d := time.Second << uint(min(attempt, 6))
	if d > time.Minute {
		return time.Minute
	}
	return d
}

func (d *Deliverer) checkTarget(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("target %q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("target %q is not http(s)", raw)
	}
	if d.AllowPrivateTargets {
		return u, nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("target host %q does not resolve: %w", u.Hostname(), err)
	}
	for _, a := range addrs {
		if !PublicIP(a.IP) {
			return nil, fmt.Errorf("target host %q now resolves to %s, which is not public; refusing to send",
				u.Hostname(), a.IP)
		}
	}
	return u, nil
}

// PublicIP reports whether an address is one this node should send an
// operator-supplied request to. Link-local is the range that matters: on every
// major cloud platform 169.254.169.254 serves instance credentials to whatever
// asks, and a webhook is exactly a "make the server fetch this" capability.
func PublicIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback(), ip.IsUnspecified(), ip.IsMulticast(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsPrivate():
		return false
	}
	return true
}

// Health is what the console shows about the loop itself, as opposed to any
// one subscription's verdict. The distinction matters for the same reason it
// does for witnessing: a subscription with nothing pending looks identical to
// one whose delivery loop died hours ago.
type Health struct {
	// Leader reports whether this replica is the one that delivers at all. A
	// follower is not broken for having done nothing.
	Leader bool `json:"leader"`
	// LastSweep is when the loop last ran. Zero means never — which is the
	// state that must not be able to hide behind a subscription that simply has
	// nothing pending.
	LastSweep time.Time `json:"last_sweep"`
	// Retrying counts consecutive failures per subscription, and LastErrors
	// says why. In memory only, so they reset on restart and on failover; that
	// is honest, because a new leader genuinely has not tried yet.
	Retrying   map[string]int    `json:"retrying,omitempty"`
	LastErrors map[string]string `json:"last_errors,omitempty"`
}

// Status snapshots the loop's own health.
func (d *Deliverer) Status() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{Leader: d.IsLeader == nil || d.IsLeader(), LastSweep: d.lastSweep}
	for id, st := range d.retry {
		if h.Retrying == nil {
			h.Retrying = map[string]int{}
			h.LastErrors = map[string]string{}
		}
		h.Retrying[id] = st.attempts
		h.LastErrors[id] = st.lastErr
	}
	return h
}
