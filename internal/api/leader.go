package api

import (
	"context"
	"net/http"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Appender is how this server writes to the log: the store directly on a
// standalone node, the Raft proposer in a cluster.
type Appender interface {
	Append(ctx context.Context, in store.AppendInput) (store.Entry, error)
}

func (s *Server) writer() Appender {
	if s.Writer != nil {
		return s.Writer
	}
	return s.Store
}

// redirectToLeader points a write at the replica that can serve it.
//
// 307 rather than 308: the redirect is about who is leader *now*, which changes
// on failover, and a permanent redirect is exactly the thing a client is
// entitled to cache. Both preserve the method and body — required here, because
// the body is what the publisher signed.
func (s *Server) redirectToLeader(w http.ResponseWriter, r *http.Request) {
	var leaderURL string
	if s.Cluster != nil {
		leaderURL = s.Cluster().LeaderURL
	}
	if leaderURL == "" {
		// No leader, or one whose public URL was never configured. Both mean
		// the write cannot proceed right now; 503 with Retry-After is honest,
		// and an election is normally over in a second or two.
		w.Header().Set("Retry-After", "2")
		writeErr(w, http.StatusServiceUnavailable, "NO_LEADER",
			"the cluster is electing a leader, or the leader's public URL is not configured")
		return
	}
	http.Redirect(w, r, leaderURL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
}
