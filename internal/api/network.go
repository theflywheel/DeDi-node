package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// networkView answers who else is on this directory network and whether they
// are answering.
//
// A network operator's first question about a directory is not "is this node
// up" — they can see that — but "how many nodes carry this network, and are
// they all serving?". Until now a node could only answer for itself, which made
// a distributed deployment indistinguishable from a single node from the
// outside.
//
// Two things are reported separately and must stay separate. Reachability is an
// observation: the peer answered. Witnessing is a proof: this node fetched the
// peer's checkpoints and consistency proofs and can demonstrate the peer has not
// rewritten history. Collapsing them would let a page that has only pinged a
// peer imply it has verified one.
func (s *Server) networkView(w http.ResponseWriter, r *http.Request) {
	nodes := []map[string]any{s.selfNode(r)}
	reachable := 1

	if s.Network != nil {
		for _, peer := range s.Network.Snapshot() {
			nodes = append(nodes, peerNode(peer))
			if peer.Reachable {
				reachable++
			}
		}
	}

	data := map[string]any{
		"nodes":       nodes,
		"total":       len(nodes),
		"reachable":   reachable,
		"witnessing":  s.WitnessTarget, // "" when this node witnesses nobody
		"witness_url": s.WitnessTargetURL,
		// Published so the claim can be checked rather than taken on trust: with
		// this, a browser can verify the target's checkpoint signature against
		// the target's own key, without this node in the loop.
		"witness_key": s.WitnessTargetKey,
	}
	if s.WitnessHealth != nil {
		data["witness_health"] = witnessHealth(s.WitnessHealth())
	}
	data["cluster"] = s.clusterView()
	ok(w, "Network retrieved successfully", data)
}

// clusterView reports this node's replication group.
//
// This is a different thing from the network above and from the witness ring,
// and the three must not be run together. The witness ring is about trust:
// independent operators proving each other's history is append-only. A cluster
// is about crash tolerance: replicas of *one* node, run by one operator, which
// therefore prove nothing about each other. A reader who confuses "three
// replicas agree" for "three parties verified" has drawn precisely the wrong
// conclusion, so the wire format keeps them in separate objects and the UI
// draws them separately.
func (s *Server) clusterView() map[string]any {
	if s.Cluster == nil {
		// An unreplicated node is a cluster of one, and says so plainly rather
		// than omitting the field — absent would be indistinguishable from a
		// node too old to report it.
		return map[string]any{"enabled": false, "size": 1, "role": "sole writer"}
	}
	st := s.Cluster()
	members := make([]map[string]any, 0, len(st.Members))
	for _, m := range st.Members {
		members = append(members, map[string]any{
			"id": m.ID, "raft_addr": m.RaftAddr, "http_url": m.HTTPURL,
			"leader": m.Leader, "self": m.Self,
		})
	}
	return map[string]any{
		"enabled":    true,
		"node_id":    st.NodeID,
		"role":       st.Role,
		"leader_id":  st.LeaderID,
		"leader_url": st.LeaderURL,
		"term":       st.Term,
		"members":    members,
		"size":       len(members),
		// A replica that is up but persistently behind is the failure a plain
		// liveness check cannot see, so the lag is published rather than
		// summarised into a green tick.
		"commit_index":  st.CommitIndex,
		"applied_index": st.AppliedIndex,
		"lag_entries":   st.LagEntries,
	}
}

// selfNode describes this node, which it can report with more confidence than
// any peer: the tree size comes from its own store rather than from a fetched
// checkpoint.
func (s *Server) selfNode(r *http.Request) map[string]any {
	name := s.NodeName
	if name == "" {
		// The origin is always set and always differs between nodes, so it is a
		// better fallback than a generic label that would make every node in the
		// panel look identical.
		name = s.origin()
	}
	node := map[string]any{
		"name":      name,
		"self":      true,
		"reachable": true,
		"origin":    s.origin(),
	}
	size, _, err := s.Store.LatestCheckpointAt(r.Context())
	switch {
	case errors.Is(err, store.ErrNoCheckpoint):
		// A node that has not signed yet is up but not yet verifiable. Reporting
		// tree_size 0 would be indistinguishable from an empty signed log.
	case err != nil:
		node["error"] = "log unreadable"
	default:
		node["tree_size"] = size
	}
	return node
}

func peerNode(peer network.Status) map[string]any {
	node := map[string]any{
		"name":      peer.Name,
		"url":       peer.URL,
		"self":      false,
		"reachable": peer.Reachable,
	}
	if !peer.CheckedAt.IsZero() {
		node["checked_at"] = peer.CheckedAt.Format(time.RFC3339)
	}
	if peer.Origin != "" {
		node["origin"] = peer.Origin
	}
	if peer.Reachable {
		node["tree_size"] = peer.TreeSize
		node["latency_ms"] = peer.LatencyMS
	}
	if peer.Error != "" {
		node["error"] = peer.Error
	}
	return node
}

// WitnessState is a witness's report on its own liveness, as supplied by the
// process running it.
type WitnessState struct {
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
	Attempts      int64
	Failures      int64
	Interval      time.Duration
	// Standby means this replica is deliberately not witnessing because it is
	// not the cluster's writer.
	Standby bool
}

// witnessHealth renders the witness's liveness for the network view.
//
// This is the node talking about itself and is not evidence of anything — the
// evidence is the verdict and its consistency proof, which a reader checks
// without this node's help. What it is for is noticing that the evidence has
// stopped being refreshed: a witness failing on every run keeps its last verdict
// frozen, still reading consistency_ok, looking exactly like one that checked a
// moment ago and found nothing new.
//
// `stale` is computed here rather than left to each caller so the page, the
// uptime monitor and anyone reading the JSON agree on what counts as too long.
// Three intervals allows a missed run and a slow one before crying wolf.
func witnessHealth(state WitnessState) map[string]any {
	health := map[string]any{
		"attempts": state.Attempts,
		"failures": state.Failures,
	}
	if state.Interval > 0 {
		health["interval_seconds"] = int64(state.Interval.Seconds())
	}
	if state.LastError != "" {
		health["last_error"] = state.LastError
	}
	if state.Standby {
		// A follower is not witnessing on purpose: the leader does it and the
		// verdict is replicated here. Without saying so, a healthy follower
		// would be indistinguishable from a stalled witness — no recent attempt,
		// no recent success — and two of every three replicas would alarm.
		health["standby"] = true
		health["checking"] = false
		health["stale"] = false
		return health
	}
	if !state.LastAttemptAt.IsZero() {
		health["last_attempt_at"] = state.LastAttemptAt.Format(time.RFC3339)
	}
	if state.LastSuccessAt.IsZero() {
		// Never completed a check. Not the same as stale-after-working, and a
		// reader should be able to tell those apart.
		health["checking"] = false
		health["stale"] = true
		return health
	}
	since := time.Since(state.LastSuccessAt)
	health["last_success_at"] = state.LastSuccessAt.Format(time.RFC3339)
	health["seconds_since_success"] = int64(since.Seconds())
	health["checking"] = true
	health["stale"] = state.Interval > 0 && since > 3*state.Interval
	return health
}

func (s *Server) origin() string {
	if s.CP == nil {
		return ""
	}
	return s.CP.Origin
}
