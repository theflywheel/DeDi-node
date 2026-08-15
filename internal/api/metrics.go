package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// /metrics answers the question the network view already answers, but to a
// machine: is this replica keeping up?
//
// The lag is on /dedi/network today, which means noticing it requires a human
// to have the page open. A replica that falls behind at 3am, or one that is up
// and reachable but no longer hearing from its leader, is exactly the failure
// nobody is watching for — so the numbers are published in the one format every
// alerting system already ingests (Prometheus text exposition, OpenMetrics'
// predecessor and still what a scrape expects by default).
//
// Hand-written rather than pulled in via the client library: this is a dozen
// gauges with no histograms, no label cardinality to manage, and no registry to
// keep in sync, against a dependency that brings its own protobuf and expvar
// surface. The format is a stable, documented three-line contract.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// metricsWriter accumulates one exposition document. Each metric is emitted as
// HELP, TYPE, then samples — a scraper tolerates a missing HELP, but an
// operator reading a raw scrape to work out what alert to write does not.
type metricsWriter struct {
	b strings.Builder
}

func (m *metricsWriter) gauge(name, help string, value float64, labels ...string) {
	m.emit("gauge", name, help, value, labels...)
}

func (m *metricsWriter) counter(name, help string, value float64, labels ...string) {
	m.emit("counter", name, help, value, labels...)
}

// emit writes one metric family with a single sample. Families with several
// samples call it repeatedly; the duplicated HELP/TYPE lines that would produce
// are suppressed by tracking what has already been declared.
func (m *metricsWriter) emit(kind, name, help string, value float64, labels ...string) {
	if !strings.Contains(m.b.String(), "# TYPE "+name+" ") {
		fmt.Fprintf(&m.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	m.b.WriteString(name)
	if len(labels) > 0 {
		pairs := make([]string, 0, len(labels)/2)
		for i := 0; i+1 < len(labels); i += 2 {
			// Not %q: escapeLabel already applies the exposition format's own
			// three-character escaping, and Go's quoting on top of it would
			// double every backslash.
			pairs = append(pairs, labels[i]+`="`+escapeLabel(labels[i+1])+`"`)
		}
		m.b.WriteString("{" + strings.Join(pairs, ",") + "}")
	}
	// %g, not %d: these are float64 gauges, and %g prints an integral value
	// without a decimal point rather than in exponent form.
	fmt.Fprintf(&m.b, " %g\n", value)
}

// escapeLabel handles the three characters the exposition format reserves in a
// label value. Node IDs and peer names are operator-supplied, so an unescaped
// quote would produce a document a scraper rejects wholesale — taking every
// other metric down with it.
func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	var m metricsWriter

	// Reachability of the database is the same fact /healthz reports, exported
	// as a gauge so "the node answers but cannot read" is alertable rather than
	// only visible to whoever opened the page.
	dbUp := s.Store.Ping(r.Context()) == nil
	m.gauge("dedi_up", "1 when this node can reach its database and serve reads.", boolGauge(dbUp))

	if !s.startedAt.IsZero() {
		m.gauge("dedi_uptime_seconds", "Seconds since this node started serving.",
			time.Since(s.startedAt).Seconds())
	}

	if size, err := s.Store.TreeSize(r.Context()); err == nil {
		m.gauge("dedi_log_entries", "Entries in this node's append-only log.", float64(size))
	}

	// Checkpoint age is reported, never alerted on by this node: the
	// checkpointer writes nothing while the tree is unchanged, so on a quiet
	// registry an hours-old checkpoint is correct. Whether that is a problem
	// depends on how busy the registry is meant to be, which is a judgement for
	// the operator's alert rule, not for us.
	size, at, err := s.Store.LatestCheckpointAt(r.Context())
	if err == nil {
		m.gauge("dedi_checkpoint_tree_size", "Tree size of the newest signed checkpoint.", float64(size))
		m.gauge("dedi_checkpoint_age_seconds", "Seconds since the newest checkpoint was signed.",
			time.Since(at).Seconds())
	} else if errors.Is(err, store.ErrNoCheckpoint) {
		// Absent rather than zero: a node that has never signed is not a node
		// whose newest checkpoint covers zero entries.
		m.gauge("dedi_checkpoint_age_seconds", "Seconds since the newest checkpoint was signed.", -1)
	}

	s.clusterMetrics(&m)
	s.requestMetrics(r, &m)
	s.peerMetrics(&m)

	w.Header().Set("Content-Type", metricsContentType)
	w.Write([]byte(m.b.String()))
}

// clusterMetrics is what task #30 is actually about. Both numbers are needed and
// neither substitutes for the other — see cluster.State.LastContactSeconds.
func (s *Server) clusterMetrics(m *metricsWriter) {
	if s.Cluster == nil {
		// An unreplicated node emits the family with enabled=0 rather than
		// omitting it. A missing series and a healthy one look identical to an
		// alert rule written as "lag > 0", so absence would silently disarm it.
		m.gauge("dedi_cluster_enabled", "1 when this node replicates its log via Raft.", 0)
		return
	}
	st := s.Cluster()
	m.gauge("dedi_cluster_enabled", "1 when this node replicates its log via Raft.", 1)
	node := []string{"node_id", st.NodeID}
	m.gauge("dedi_cluster_is_leader", "1 when this replica is the Raft leader.",
		boolGauge(st.Role == "leader"), node...)
	m.gauge("dedi_cluster_term",
		"Current Raft term. A term that keeps climbing is a cluster that keeps re-electing.",
		float64(st.Term), node...)
	m.gauge("dedi_cluster_commit_index", "Highest log index committed by the cluster.",
		float64(st.CommitIndex), node...)
	m.gauge("dedi_cluster_applied_index", "Highest log index applied to this replica's state.",
		float64(st.AppliedIndex), node...)
	m.gauge("dedi_cluster_lag_entries",
		"Committed entries this replica has not applied yet. Persistently non-zero means it is falling behind.",
		float64(st.LagEntries), node...)
	m.gauge("dedi_cluster_last_contact_seconds",
		"Seconds since the leader last reached this follower; 0 on the leader, -1 if it has never heard from one.",
		st.LastContactSeconds, node...)
	m.gauge("dedi_cluster_members", "Replicas in the current Raft configuration.",
		float64(len(st.Members)), node...)
	// A cluster that cannot see a leader cannot accept writes at all. It is a
	// brief and normal state during an election, so the alert belongs on its
	// duration — which needs the series to exist even while it is healthy.
	m.gauge("dedi_cluster_has_leader", "1 when this replica currently knows who the leader is.",
		boolGauge(st.LeaderID != ""), node...)
}

func (s *Server) requestMetrics(r *http.Request, m *metricsWriter) {
	persisted, err := s.Store.RequestCounts(r.Context())
	if err != nil {
		return
	}
	// Persisted plus the unflushed window, the same sum /dedi/stats reports, so
	// the two surfaces never disagree by a flush interval.
	for _, class := range counterClasses {
		m.counter("dedi_requests_total", "Requests served, by HTTP status class.",
			float64(persisted[class]+s.reqs.bucket(class).Load()), "status_class", class)
	}
}

// peerMetrics exports what this node observes of the rest of the network. A
// witness ring whose peers have quietly become unreachable still renders as a
// ring in the UI; here it is a gauge that can page someone.
func (s *Server) peerMetrics(m *metricsWriter) {
	if s.Network == nil {
		return
	}
	for _, p := range s.Network.Snapshot() {
		m.gauge("dedi_peer_reachable", "1 when this node's last poll of a network peer succeeded.",
			boolGauge(p.Reachable), "peer", p.Name, "url", p.URL)
	}
}
