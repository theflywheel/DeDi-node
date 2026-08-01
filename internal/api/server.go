package api

import (
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Server struct {
	Store       *store.Store
	CP          *checkpoint.Checkpointer
	TTL         int    // cache hint surfaced in ttl fields (seconds)
	VerifierKey string // node verifier key, injected into the explorer page (may be empty)

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
	mux.HandleFunc("GET /{$}", s.explorer)
	mux.HandleFunc("GET /docs", s.docs)
	mux.HandleFunc("GET /docs/{$}", s.docs)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		notFound(w, "route")
	})
	return s.counted(mux)
}
