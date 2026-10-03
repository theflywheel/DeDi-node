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

// onFollower reports whether this node is a clustered replica that is not the
// leader, and therefore must not decide anything about a write from its own
// state.
//
// A follower's store is a replica of the log as of whatever it has applied,
// which by construction may trail the leader. That is fine for reads — they are
// answered as of a checkpoint and say so — but a write decision made from it is
// a claim about the current version, and the follower is not the authority on
// that.
func (s *Server) onFollower() bool {
	if s.Cluster == nil {
		return false
	}
	st := s.Cluster()
	return st.Enabled && st.Role != "leader"
}

// leaderOnly redirects a write that reached a follower before its handler runs.
//
// Handlers read the replica before they write: whether the record exists,
// whether the child is already delegated, whether the offer has been applied.
// On a follower that trails the leader those reads answer 404, 409 or 401 for a
// request the leader would accept. Redirecting here, once, is what keeps every
// write route from having to remember (#86). GETs on the write plane are reads
// and stay local.
func (s *Server) leaderOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && s.onFollower() {
			s.redirectToLeader(w, r)
			return
		}
		h(w, r)
	}
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
