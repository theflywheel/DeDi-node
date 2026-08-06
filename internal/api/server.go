package api

import (
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Server struct {
	Store       *store.Store
	CP          *checkpoint.Checkpointer
	TTL         int    // cache hint surfaced in ttl fields (seconds)
	VerifierKey string // node verifier key, injected into the explorer page (may be empty)
	DemoURL     string // target of the pages' "Demo" nav tab; empty falls back to defaultDemoURL

	// NodeName labels this node in the network view. Empty falls back to the
	// log origin, which is always set and always distinct between nodes.
	NodeName string

	// Network observes the other nodes carrying this directory network. nil on
	// a standalone node, which then reports a network of one — accurately.
	Network *network.Monitor

	// WitnessTarget and WitnessTargetURL name the node this one witnesses, so
	// the network view can distinguish a peer it merely reaches from the peer
	// whose history it is actually proving. Empty when witnessing nothing.
	WitnessTarget    string
	WitnessTargetURL string

	// WitnessTargetKey is the target's public verifier key. It is published so a
	// visitor's browser can check the target's checkpoint signature itself
	// rather than believing this node's report of it. Public by construction —
	// a verifier key is what you hand out precisely so others can check you.
	WitnessTargetKey string

	// WitnessHealth reports whether the witness loop is still running. Supplied
	// as a function rather than a *witness.Witness so this package does not
	// import witness — witness's own tests import this one, and the cycle would
	// not build.
	WitnessHealth func() WitnessState

	// Writer appends to the log. nil writes straight to Store, which is the
	// standalone case; in a cluster it is the Raft proposer, and writes that
	// arrive at a follower are redirected to the leader rather than applied
	// locally.
	Writer Appender

	// Cluster reports Raft membership and leadership, for the network view and
	// for redirecting writes. nil on an unreplicated node, which then reports a
	// cluster of one — accurately.
	Cluster func() cluster.State

	// WildcardNamespaces limits which namespaces may answer a Beckn wildcard
	// lookup (design.md:256). nil means no restriction — permitted only while
	// the write plane is closed; see serve().
	WildcardNamespaces []string

	// Auth verifies signed writes. nil, or holding no keys, leaves the write
	// plane closed and its routes unregistered.
	Auth *publisher.Authenticator

	reqs      counters  // requests served since the last flush (see counter.go)
	startedAt time.Time // set by Handler
}

func (s *Server) Handler() http.Handler {
	s.startedAt = time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dedi/lookup/{namespace}", s.lookupNamespace)
	mux.HandleFunc("GET /dedi/lookup/{namespace}/{registry_name}", s.lookupRegistry)
	mux.HandleFunc("GET /dedi/lookup/{namespace}/{registry_name}/{record_name}", s.lookupRecord)
	mux.HandleFunc("GET /dedi/query/{namespace}", s.queryNamespace)
	mux.HandleFunc("GET /dedi/query/{namespace}/{registry_name}", s.queryRegistry)
	mux.HandleFunc("GET /dedi/versions/{namespace}", s.versionsNamespace)
	mux.HandleFunc("GET /dedi/versions/{namespace}/{registry_name}", s.versionsRegistry)
	mux.HandleFunc("GET /dedi/versions/{namespace}/{registry_name}/{record_name}", s.versionsRecord)
	mux.HandleFunc("GET /dedi/log/checkpoint", s.logCheckpoint)
	mux.HandleFunc("GET /dedi/log/proof/consistency", s.logConsistency)
	mux.HandleFunc("GET /dedi/stats", s.stats)
	mux.HandleFunc("GET /dedi/network", s.networkView)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /{$}", s.explorer)
	mux.HandleFunc("GET /verify", s.verify)
	mux.HandleFunc("GET /verify/{$}", s.verify)
	mux.HandleFunc("GET /static/verify.js", s.verifyScript)
	mux.HandleFunc("GET /docs", s.docs)
	mux.HandleFunc("GET /docs/{$}", s.docs)
	mux.HandleFunc("GET /admin", s.admin)
	mux.HandleFunc("GET /admin/{$}", s.admin)

	// Publisher plane. Registered only when the node holds publisher keys, so a
	// read-only node has no write surface to probe at all (governance.md:
	// "enforcement today is structural").
	if s.writeEnabled() {
		write := func(pattern string, h http.HandlerFunc) {
			mux.Handle(pattern, s.Auth.Require(h, denyWrite))
		}
		write("PUT /admin/namespaces/{namespace}", s.putNamespace)
		write("PUT /admin/namespaces/{namespace}/registries/{registry_name}", s.putRegistry)
		write("POST /admin/namespaces/{namespace}/registries/{registry_name}/records/{record_name}/publish", s.publishRecord)
		write("POST /admin/namespaces/{namespace}/registries/{registry_name}/records/{record_name}/revoke", s.revokeRecord)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		notFound(w, "route")
	})
	// CORS sits outside the counter so the header is present on every response
	// the counter sees, including the ones it does not count.
	return readPlaneCORS(s.counted(mux))
}
