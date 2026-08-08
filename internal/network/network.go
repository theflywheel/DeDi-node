// Package network tracks the other nodes making up the same directory network.
//
// This is deliberately the weaker of the two things a node knows about its
// peers. Witnessing (internal/witness) is a cryptographic claim: it fetches
// signed checkpoints and consistency proofs and can prove a peer rewrote
// history. What lives here is an observation: the peer answered, and said its
// tree is this big. It cannot detect dishonesty and does not try to.
//
// Both are worth showing, and they must not be shown as the same thing — a page
// that renders "reachable" and "proven append-only" identically would claim
// something it has not established.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Peer is another node on this network, as configured by the operator.
type Peer struct {
	Name string // short label for display, e.g. "node-b"
	URL  string // base URL, no trailing slash, e.g. https://node-b.example.org
}

// Status is the most recent observation of one peer.
type Status struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Reachable bool      `json:"reachable"`
	Origin    string    `json:"origin,omitempty"`     // log origin from its checkpoint
	TreeSize  int64     `json:"tree_size,omitempty"`  // entries in its log
	LatencyMS int64     `json:"latency_ms,omitempty"` // round trip of the last probe
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

// Monitor polls the configured peers and keeps the latest observation of each.
//
// Observations are held in memory rather than the log. They are not facts about
// the directory, they are transient facts about the world, and writing them to
// an append-only log would grow it without bound in exchange for a history
// nobody audits.
type Monitor struct {
	Peers    []Peer
	Interval time.Duration
	Client   *http.Client

	mu     sync.RWMutex
	latest map[string]Status
}

// ParsePeers reads the peer list from its configured form: a comma-separated
// list of entries, each either "url" or "name=url".
//
// A node is not required to know the whole network, and an empty list is a
// legitimate configuration — a standalone node has no peers, and that is a
// deployment mode rather than a misconfiguration.
func ParsePeers(spec string) ([]Peer, error) {
	var peers []Peer
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, rawURL := "", entry
		if key, value, found := strings.Cut(entry, "="); found {
			name, rawURL = strings.TrimSpace(key), strings.TrimSpace(value)
		}
		rawURL = strings.TrimRight(rawURL, "/")
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			return nil, fmt.Errorf("peer %q: want an http(s) URL", entry)
		}
		if name == "" {
			// Fall back to the host, so an operator who lists bare URLs still
			// gets something readable rather than a full URL as a label.
			name = strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
		}
		peers = append(peers, Peer{Name: name, URL: rawURL})
	}
	return peers, nil
}

func (m *Monitor) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	// Short: this is a status probe, and a peer that takes longer than this to
	// answer is already degraded from the point of view of anyone relying on it.
	return &http.Client{Timeout: 10 * time.Second}
}

func (m *Monitor) interval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return 30 * time.Second
}

// Add registers a peer discovered at runtime — today, a child node completing
// enrolment.
//
// Peers used to be fixed at boot, and a set that only changes on restart was
// fine while the only source was configuration. A delegation is granted while
// the node is running, and an operator who has just enrolled a child and sees
// nothing in the network panel reads that as the enrolment having failed.
//
// Adding an existing peer is a no-op rather than a duplicate: enrolment is
// idempotent from the child's side, and a child that retries should not appear
// twice.
func (m *Monitor) Add(name, url string) {
	if name == "" || url == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.Peers {
		if p.URL == url {
			return
		}
	}
	m.Peers = append(m.Peers, Peer{Name: name, URL: url})
}

// peers copies the peer set under the lock. Callers iterate the copy, because
// polling one peer takes seconds and holding the lock for that would block
// every enrolment and every read of the network panel behind it.
// Remove drops a peer and forgets its last status.
//
// Forgetting matters: a revoked child left in the status map would keep
// reporting whatever it was last seen doing, and a node this one has stopped
// vouching for should disappear from the view rather than linger as a healthy
// green row nobody is checking any more.
func (m *Monitor) Remove(url string) {
	if url == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.Peers[:0]
	for _, p := range m.Peers {
		if p.URL != url {
			kept = append(kept, p)
		}
	}
	m.Peers = kept
	delete(m.latest, url)
}

func (m *Monitor) peers() []Peer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Peer(nil), m.Peers...)
}

// Run polls every peer until ctx is cancelled.
//
// It keeps running with no peers configured, because a node can gain one
// without restarting: a standalone node that delegates a child acquires its
// first peer at that moment, and a loop that had exited would never see it.
func (m *Monitor) Run(ctx context.Context) {
	// Probe immediately: waiting a full interval would leave the network panel
	// blank for the first half minute after every restart, which reads as an
	// outage rather than as a node that has not looked yet.
	m.pollAll(ctx)
	ticker := time.NewTicker(m.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollAll(ctx)
		}
	}
}

func (m *Monitor) pollAll(ctx context.Context) {
	// Concurrently, so one unreachable peer sitting on the client timeout does
	// not delay the observation of every peer behind it in the list.
	var wg sync.WaitGroup
	peers := m.peers()
	results := make([]Status, len(peers))
	for i, peer := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = m.poll(ctx, peer)
		}()
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil {
		m.latest = make(map[string]Status, len(results))
	}
	for _, status := range results {
		m.latest[status.URL] = status
	}
}

// poll asks one peer for its signed checkpoint.
//
// The checkpoint is the probe rather than /healthz because it is the stronger
// signal: it proves the peer is serving its log, not merely that its process is
// running, and it carries the tree size that makes the panel worth reading.
func (m *Monitor) poll(ctx context.Context, peer Peer) Status {
	status := Status{Name: peer.Name, URL: peer.URL, CheckedAt: time.Now().UTC()}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, peer.URL+"/dedi/log/checkpoint", nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	started := time.Now()
	resp, err := m.client().Do(req)
	if err != nil {
		status.Error = "unreachable"
		return status
	}
	defer resp.Body.Close()
	status.LatencyMS = time.Since(started).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		status.Error = fmt.Sprintf("checkpoint returned %d", resp.StatusCode)
		return status
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		status.Error = "truncated response"
		return status
	}
	// A C2SP checkpoint opens with the origin line, then the tree size.
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 2 {
		status.Error = "malformed checkpoint"
		return status
	}
	status.Origin = lines[0]
	if _, err := fmt.Sscanf(lines[1], "%d", &status.TreeSize); err != nil {
		status.Error = "malformed checkpoint size"
		return status
	}
	status.Reachable = true
	return status
}

// Snapshot returns the latest observation of every peer, in configured order.
// Peers not yet polled appear as unreachable with no error, which is honest:
// nothing is known about them yet.
func (m *Monitor) Snapshot() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.Peers))
	for _, peer := range m.Peers {
		if status, ok := m.latest[peer.URL]; ok {
			out = append(out, status)
			continue
		}
		out = append(out, Status{Name: peer.Name, URL: peer.URL})
	}
	return out
}

// MarshalSnapshot is a convenience for handlers.
func (m *Monitor) MarshalSnapshot() ([]byte, error) { return json.Marshal(m.Snapshot()) }
