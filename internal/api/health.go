package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// healthz answers whether this node can actually serve, for load balancers and
// external probes. It deliberately does not use the DeDi response envelope: it
// is an operational endpoint rather than part of the read plane spec, and probes
// want a flat document they can assert on without unwrapping.
//
// Liveness here means one thing — the database is reachable. Every read path
// goes through it, so a node whose pool has gone away answers 200 on its static
// pages while failing every lookup. That is the failure this endpoint exists to
// catch, and it is why the probe issues a real round trip rather than trusting
// that the pool was healthy at startup.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	// Bound the probe: a hung database should fail the check quickly rather than
	// hold the prober open until its own timeout fires.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	body := map[string]any{"status": "ok", "database": "ok"}
	if !s.startedAt.IsZero() {
		body["uptime_seconds"] = int64(time.Since(s.startedAt).Seconds())
	}

	if err := s.Store.Ping(ctx); err != nil {
		body["status"] = "unavailable"
		body["database"] = "unreachable"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}

	// Log state is reported, never gated on. The checkpointer is idempotent — it
	// writes no new row while the tree is unchanged — so on a quiet registry the
	// newest checkpoint is legitimately hours old. Failing health on its age
	// would page an operator every time nobody published anything.
	size, at, err := s.Store.LatestCheckpointAt(ctx)
	switch {
	case errors.Is(err, store.ErrNoCheckpoint):
		body["checkpoint"] = "none published"
	case err != nil:
		// The log is degraded but lookups still work; say so without failing.
		body["checkpoint"] = "unreadable"
	default:
		body["tree_size"] = size
		body["checkpoint_age_seconds"] = int64(time.Since(at).Seconds())
	}

	writeJSON(w, http.StatusOK, body)
}
