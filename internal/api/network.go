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
	}
	ok(w, "Network retrieved successfully", data)
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

func (s *Server) origin() string {
	if s.CP == nil {
		return ""
	}
	return s.CP.Origin
}
