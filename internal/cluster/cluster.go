package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	boltdb "github.com/hashicorp/raft-boltdb/v2"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// ErrNotLeader is returned by every write path on a replica that is not the
// leader. Callers turn it into a redirect rather than an error page: the
// cluster is available, this node just is not the one that appends.
var ErrNotLeader = errors.New("not the leader")

// Peer is a member of the cluster as configured by the operator.
type Peer struct {
	ID       string // stable, unique; survives restarts and address changes
	RaftAddr string // host:port for the Raft transport
	HTTPURL  string // public base URL, used to redirect writes to the leader
}

// ParsePeers reads the cluster layout from a config string of
// `id=raft-host:port=https://public-url` entries separated by commas or
// whitespace. The HTTP URL is optional; without it a follower can say who the
// leader is but cannot redirect a client to it.
func ParsePeers(spec string) ([]Peer, error) {
	var out []Peer
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		parts := strings.Split(field, "=")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("peer %q: want id=host:port[=http-url]", field)
		}
		p := Peer{ID: strings.TrimSpace(parts[0]), RaftAddr: strings.TrimSpace(parts[1])}
		if len(parts) == 3 {
			p.HTTPURL = strings.TrimRight(strings.TrimSpace(parts[2]), "/")
		}
		if p.ID == "" || p.RaftAddr == "" {
			return nil, fmt.Errorf("peer %q: empty id or address", field)
		}
		if _, _, err := net.SplitHostPort(p.RaftAddr); err != nil {
			return nil, fmt.Errorf("peer %q: %w", field, err)
		}
		if seen[p.ID] {
			// Two members sharing an ID is not a typo Raft can survive: they
			// would vote as one another.
			return nil, fmt.Errorf("peer id %q appears twice", p.ID)
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	return out, nil
}

type Config struct {
	ID            string        // this node's peer ID; must appear in Peers
	BindAddr      string        // host:port to listen on, defaults to the peer's RaftAddr
	DataDir       string        // durable directory for the Raft log and snapshots
	Peers         []Peer        // the whole cluster, including this node
	Bootstrap     bool          // form the cluster from Peers; set on exactly one node, once
	ApplyTimeout  time.Duration // how long a proposal may take to commit
	LogOutput     io.Writer     // Raft's own logging; nil discards it
	OnHalt        func(error)   // called when this replica cannot apply a committed entry
	SnapshotRetin int           // snapshots to keep; 0 uses a sensible default
}

// Node is this process's membership in the cluster, and the only way it
// writes to the log.
type Node struct {
	raft  *raft.Raft
	store *store.Store
	cfg   Config
	peers map[string]Peer

	transport *raft.NetworkTransport
	stable    *boltdb.BoltStore
}

// Open starts Raft and begins applying committed commands into s.
//
// Every replica materialises into its own database. Two replicas sharing one
// would each apply every command to the same rows and corrupt it immediately,
// so this is not a configuration to get subtly wrong — see docs/replication.md.
func Open(cfg Config, s *store.Store) (*Node, error) {
	if cfg.ID == "" {
		return nil, errors.New("cluster: node id is required")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("cluster: data dir is required")
	}
	peers := map[string]Peer{}
	for _, p := range cfg.Peers {
		peers[p.ID] = p
	}
	self, ok := peers[cfg.ID]
	if !ok {
		return nil, fmt.Errorf("cluster: node id %q is not in the peer list", cfg.ID)
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = self.RaftAddr
	}
	if cfg.ApplyTimeout <= 0 {
		cfg.ApplyTimeout = 10 * time.Second
	}
	if cfg.SnapshotRetin <= 0 {
		cfg.SnapshotRetin = 3
	}
	if cfg.OnHalt == nil {
		return nil, errors.New("cluster: OnHalt is required — a replica that cannot apply a " +
			"committed entry must stop, not continue with a divergent view")
	}
	logOut := cfg.LogOutput
	if logOut == nil {
		logOut = io.Discard
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("cluster: data dir: %w", err)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.ID)
	rc.LogOutput = logOut

	advertise, err := net.ResolveTCPAddr("tcp", self.RaftAddr)
	if err != nil {
		return nil, fmt.Errorf("cluster: resolve advertised address %q: %w", self.RaftAddr, err)
	}
	transport, err := raft.NewTCPTransport(cfg.BindAddr, advertise, 3, 10*time.Second, logOut)
	if err != nil {
		return nil, fmt.Errorf("cluster: transport on %s: %w", cfg.BindAddr, err)
	}

	stable, err := boltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft.db"))
	if err != nil {
		transport.Close()
		return nil, fmt.Errorf("cluster: raft store: %w", err)
	}
	snaps, err := raft.NewFileSnapshotStore(cfg.DataDir, cfg.SnapshotRetin, logOut)
	if err != nil {
		stable.Close()
		transport.Close()
		return nil, fmt.Errorf("cluster: snapshot store: %w", err)
	}

	f := &fsm{store: s, halt: cfg.OnHalt}
	r, err := raft.NewRaft(rc, f, stable, stable, snaps, transport)
	if err != nil {
		stable.Close()
		transport.Close()
		return nil, fmt.Errorf("cluster: start raft: %w", err)
	}

	n := &Node{raft: r, store: s, cfg: cfg, peers: peers, transport: transport, stable: stable}

	if cfg.Bootstrap {
		servers := make([]raft.Server, 0, len(cfg.Peers))
		for _, p := range cfg.Peers {
			servers = append(servers, raft.Server{
				ID:      raft.ServerID(p.ID),
				Address: raft.ServerAddress(p.RaftAddr),
			})
		}
		// Bootstrapping an already-formed cluster returns ErrCantBootstrap, which
		// is the expected result on every restart after the first. Treating it as
		// fatal would mean a node could only ever start once.
		if err := r.BootstrapCluster(raft.Configuration{Servers: servers}).Error(); err != nil &&
			!errors.Is(err, raft.ErrCantBootstrap) {
			n.Close()
			return nil, fmt.Errorf("cluster: bootstrap: %w", err)
		}
	}
	return n, nil
}

func (n *Node) Close() error {
	var err error
	if n.raft != nil {
		err = n.raft.Shutdown().Error()
	}
	if n.transport != nil {
		n.transport.Close()
	}
	if n.stable != nil {
		n.stable.Close()
	}
	return err
}

// Append proposes an entry and waits for it to commit and apply.
//
// The timestamp is stamped here, once, by the proposer — it is hashed into the
// Merkle leaf, so it cannot be re-derived per replica (store.AppendInput.CreatedAt).
func (n *Node) Append(ctx context.Context, in store.AppendInput) (store.Entry, error) {
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	res, err := n.propose(ctx, command{Kind: cmdAppend, Input: &in})
	if err != nil {
		return store.Entry{}, err
	}
	switch v := res.(type) {
	case store.Entry:
		return v, nil
	case error:
		// A deterministic rejection: the command committed, every replica
		// rejected it identically, and the client gets its 400 or 409.
		return store.Entry{}, v
	default:
		return store.Entry{}, fmt.Errorf("cluster: unexpected apply result %T", res)
	}
}

// SignCheckpoint replicates an already-signed tree head. The signing happens
// on the leader, over the leader's committed state; replicating the signed
// note rather than the instruction to sign means followers store exactly the
// bytes that were signed and never need the identity key.
func (n *Node) SignCheckpoint(ctx context.Context, size int64, root []byte, note string) error {
	res, err := n.propose(ctx, command{
		Kind: cmdSignCheckpoint, TreeSize: size, RootHash: root, NoteText: note,
	})
	if err != nil {
		return err
	}
	if e, ok := res.(error); ok {
		return e
	}
	return nil
}

func (n *Node) propose(ctx context.Context, c command) (any, error) {
	if n.raft.State() != raft.Leader {
		return nil, ErrNotLeader
	}
	b, err := encodeCommand(c)
	if err != nil {
		return nil, err
	}
	timeout := n.cfg.ApplyTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	fut := n.raft.Apply(b, timeout)
	if err := fut.Error(); err != nil {
		if errors.Is(err, raft.ErrNotLeader) || errors.Is(err, raft.ErrLeadershipLost) ||
			errors.Is(err, raft.ErrLeadershipTransferInProgress) {
			// Leadership moved between the check above and the proposal. The
			// write did not happen; the client should retry against the new
			// leader rather than be told the node is broken.
			return nil, ErrNotLeader
		}
		return nil, fmt.Errorf("cluster: propose %s: %w", c.Kind, err)
	}
	return fut.Response(), nil
}

func (n *Node) IsLeader() bool { return n.raft.State() == raft.Leader }

// Leader reports the current leader's peer ID and public URL. Both are empty
// during an election, which is a normal and brief state.
func (n *Node) Leader() (id string, httpURL string) {
	addr, serverID := n.raft.LeaderWithID()
	if serverID == "" {
		return "", ""
	}
	if p, ok := n.peers[string(serverID)]; ok {
		return string(serverID), p.HTTPURL
	}
	return string(serverID), string(addr)
}

// Member is one replica as Raft currently sees it.
type Member struct {
	ID       string `json:"id"`
	RaftAddr string `json:"raft_addr"`
	HTTPURL  string `json:"http_url,omitempty"`
	Leader   bool   `json:"leader"`
	Self     bool   `json:"self"`
}

// State describes the cluster for the network view and health checks.
type State struct {
	Enabled   bool     `json:"enabled"`
	NodeID    string   `json:"node_id"`
	Role      string   `json:"role"` // leader | follower | candidate
	LeaderID  string   `json:"leader_id,omitempty"`
	LeaderURL string   `json:"leader_url,omitempty"`
	Members   []Member `json:"members"`
	// Term counts elections. A term that keeps climbing is a cluster that keeps
	// re-electing — the symptom of a flapping replica or a partition, and
	// invisible in any single snapshot of who happens to be leader.
	Term         uint64 `json:"term"`
	CommitIndex  uint64 `json:"commit_index"`
	AppliedIndex uint64 `json:"applied_index"`
	// LagEntries is how far this replica's applied state trails what has been
	// committed. Persistently non-zero is the signal that a replica is falling
	// behind, which a bare "is it up" check would miss entirely.
	LagEntries uint64 `json:"lag_entries"`
}

func (n *Node) State() State {
	leaderID, leaderURL := n.Leader()
	st := State{
		Enabled:      true,
		NodeID:       n.cfg.ID,
		Role:         strings.ToLower(n.raft.State().String()),
		LeaderID:     leaderID,
		LeaderURL:    leaderURL,
		Term:         n.raft.CurrentTerm(),
		CommitIndex:  n.raft.CommitIndex(),
		AppliedIndex: n.raft.AppliedIndex(),
	}
	if st.CommitIndex > st.AppliedIndex {
		st.LagEntries = st.CommitIndex - st.AppliedIndex
	}
	if cfg := n.raft.GetConfiguration(); cfg.Error() == nil {
		for _, srv := range cfg.Configuration().Servers {
			id := string(srv.ID)
			st.Members = append(st.Members, Member{
				ID:       id,
				RaftAddr: string(srv.Address),
				HTTPURL:  n.peers[id].HTTPURL,
				Leader:   id == leaderID,
				Self:     id == n.cfg.ID,
			})
		}
		sort.Slice(st.Members, func(i, j int) bool { return st.Members[i].ID < st.Members[j].ID })
	}
	return st
}
