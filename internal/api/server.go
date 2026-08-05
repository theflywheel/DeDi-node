package api

import (
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Server struct {
	Store       *store.Store
	CP          *checkpoint.Checkpointer
	TTL         int    // cache hint surfaced in ttl fields (seconds)
	VerifierKey string // node verifier key, injected into the explorer page (may be empty)
	DemoURL     string // target of the pages' "Demo" nav tab; empty falls back to defaultDemoURL

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
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /{$}", s.explorer)
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
	return s.counted(mux)
}
