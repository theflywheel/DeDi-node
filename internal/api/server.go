package api

import (
	"net/http"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Server struct {
	Store *store.Store
	CP    *checkpoint.Checkpointer
	TTL   int // cache hint surfaced in ttl fields (seconds)
}

func (s *Server) Handler() http.Handler {
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		notFound(w, "route")
	})
	return mux
}

// Placeholder handlers replaced in Tasks 8–11. Registered now so the route
// table is complete and tested once.
func (s *Server) lookupNamespace(w http.ResponseWriter, r *http.Request)   { notImplemented(w) }
func (s *Server) lookupRegistry(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (s *Server) lookupRecord(w http.ResponseWriter, r *http.Request)      { notImplemented(w) }
func (s *Server) queryNamespace(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (s *Server) queryRegistry(w http.ResponseWriter, r *http.Request)     { notImplemented(w) }
func (s *Server) versionsNamespace(w http.ResponseWriter, r *http.Request) { notImplemented(w) }
func (s *Server) versionsRegistry(w http.ResponseWriter, r *http.Request)  { notImplemented(w) }
func (s *Server) versionsRecord(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (s *Server) logConsistency(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }

func notImplemented(w http.ResponseWriter) {
	writeErr(w, http.StatusInternalServerError, "INTERNAL", "not implemented")
}
