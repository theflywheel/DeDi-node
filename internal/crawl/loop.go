package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Where the crawler keeps its own state: which key each domain was last seen
// signing with, and how the last attempt went. An underscore namespace, so it
// is hidden from the read plane and never published in our own files.
const (
	crawlNS       = "_crawl"
	crawlRegistry = "sources"
)

// sourceState is the part of a crawled domain's bookkeeping that belongs in the
// log: durable facts about the publisher, not observations about our last
// attempt.
//
// PinnedKey is the load-bearing field. It is what turns "this manifest is
// internally consistent" into "this manifest is from the same publisher as last
// time", which is the only continuity a self-signed manifest can offer — see
// Fetcher.verifyManifest.
//
// Deliberately absent: when we last crawled, and what that crawl ingested.
// Those change on every tick, and writing them here would append one entry per
// domain per interval forever — the same unbounded growth Ingest is careful to
// avoid. They live in Health instead, in memory, the way webhook delivery keeps
// its own liveness.
type sourceState struct {
	Domain    string `json:"domain"`
	PinnedKey string `json:"pinned_key"`
	KeyID     string `json:"key_id,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// SourceHealth is what the last attempt against one domain looked like. In
// memory only, so it resets on restart and on failover — which is honest,
// because a new leader genuinely has not crawled yet.
type SourceHealth struct {
	Domain    string    `json:"domain"`
	LastCrawl time.Time `json:"last_crawl"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	PinnedKey string    `json:"pinned_key,omitempty"`
}

// Loop crawls a fixed set of domains on a cadence.
type Loop struct {
	Store    *store.Store
	Writer   Appender
	Fetcher  *Fetcher
	Domains  []string
	Interval time.Duration

	// IsWriter gates the loop in a cluster. Only the leader crawls: three
	// replicas each fetching the same directory would each propose the same
	// writes, and the two that lost would have burned a fetch to learn a fact
	// the log already had.
	IsWriter func() bool

	// Now is injectable for tests.
	Now func() time.Time

	mu     sync.Mutex
	health map[string]SourceHealth
}

// Health snapshots the last attempt against every configured domain.
func (l *Loop) Health() []SourceHealth {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]SourceHealth, 0, len(l.health))
	for _, d := range l.Domains {
		if h, ok := l.health[d]; ok {
			out = append(out, h)
		}
	}
	return out
}

func (l *Loop) note(h SourceHealth) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.health == nil {
		l.health = map[string]SourceHealth{}
	}
	l.health[h.Domain] = h
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now().UTC()
}

// Run crawls every configured domain immediately, then on the interval, until
// ctx is done.
func (l *Loop) Run(ctx context.Context) {
	if len(l.Domains) == 0 {
		return
	}
	interval := l.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		l.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once crawls every configured domain a single time.
//
// One domain's failure never stops the others: a partner whose certificate
// expired must not take our whole view of the network offline. Each failure is
// recorded against its own source instead.
func (l *Loop) Once(ctx context.Context) {
	if l.IsWriter != nil && !l.IsWriter() {
		return
	}
	for _, domain := range l.Domains {
		if err := l.crawlOne(ctx, domain); err != nil {
			log.Printf("crawl %s: %v", domain, err)
		}
	}
}

func (l *Loop) crawlOne(ctx context.Context, domain string) error {
	state, _ := l.state(ctx, domain)
	state.Domain = domain

	res, err := l.Fetcher.Fetch(ctx, domain, state.PinnedKey)
	if err != nil {
		state.LastError = err.Error()
		l.note(SourceHealth{Domain: domain, LastCrawl: l.now(), Error: err.Error(), PinnedKey: state.PinnedKey})
		// A key change is recorded, not swallowed: the pin stays as it was, so
		// the loop keeps refusing until an operator looks and either confirms
		// the rotation or treats it as the takeover it might be.
		_ = l.saveState(ctx, state, errors.Is(err, ErrKeyChanged))
		return err
	}

	ing := &Ingester{Store: l.Store, Writer: l.Writer}
	n, err := ing.Ingest(ctx, res)
	if err != nil {
		state.LastError = err.Error()
		l.note(SourceHealth{Domain: domain, LastCrawl: l.now(), Result: n.String(), Error: err.Error(), PinnedKey: state.PinnedKey})
		_ = l.saveState(ctx, state, false)
		return err
	}
	// Trust on first use: the first successful crawl is what establishes the
	// pin. It proves nothing on its own — an attacker present at that moment
	// gets pinned instead — but from then on every silent key change is caught,
	// which is the property §14 asks a monitor for.
	state.PinnedKey, state.KeyID = res.Manifest.Keys[0].X, res.KeyID
	state.LastError = ""
	l.note(SourceHealth{Domain: domain, LastCrawl: l.now(), Result: n.String(), PinnedKey: state.PinnedKey})
	if n.Changed() {
		log.Printf("crawl %s: ingested %s", domain, n)
	}
	return l.saveState(ctx, state, false)
}

// state reads a domain's bookkeeping, or a zero value if we have never crawled it.
func (l *Loop) state(ctx context.Context, domain string) (sourceState, bool) {
	e, err := l.Store.Resolve(ctx, "record", crawlNS, crawlRegistry, domain, nil, nil)
	if err != nil {
		return sourceState{}, false
	}
	var st sourceState
	if json.Unmarshal(e.PayloadRaw, &st) != nil {
		return sourceState{}, false
	}
	return st, true
}

// saveState writes the bookkeeping entry, skipping the write when nothing
// changed — otherwise a loop crawling an unchanged publisher hourly would still
// append one entry per domain per hour, which is exactly the unbounded growth
// Ingest is careful to avoid.
func (l *Loop) saveState(ctx context.Context, st sourceState, alarm bool) error {
	if err := l.ensureParents(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return err
	}
	state := "live"
	if alarm {
		// A pinned key that stopped matching is surfaced as a revoked source,
		// so it reads as broken on every surface that already renders state
		// rather than needing its own alert path.
		state = "revoked"
	}
	if prev, err := l.Store.Resolve(ctx, "record", crawlNS, crawlRegistry, st.Domain, nil, nil); err == nil {
		var old sourceState
		if json.Unmarshal(prev.PayloadRaw, &old) == nil && old == st && prev.State == state {
			return nil
		}
	}
	_, err = l.Writer.Append(ctx, store.AppendInput{
		EntryType: "record", Namespace: crawlNS, Registry: crawlRegistry,
		RecordName: st.Domain, PayloadRaw: payload, State: state,
		CreatedBy: "crawler",
	})
	return err
}

func (l *Loop) ensureParents(ctx context.Context) error {
	if _, err := l.Store.Resolve(ctx, "namespace", crawlNS, "", "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if _, err := l.Writer.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: crawlNS,
			PayloadRaw: []byte(`{"description":"publishers this node crawls, and the keys they sign with"}`),
			CreatedBy:  "crawler"}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := l.Store.Resolve(ctx, "registry", crawlNS, crawlRegistry, "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if _, err := l.Writer.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: crawlNS,
			Registry: crawlRegistry, PayloadRaw: []byte(`{"description":"crawl state and pinned manifest keys"}`),
			CreatedBy: "crawler"}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}
