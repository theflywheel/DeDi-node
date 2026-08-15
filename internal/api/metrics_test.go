package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/network"
)

// scrape fetches /metrics and parses it the way a scraper would: into a map
// from a full series line's name-and-labels to its value.
func scrape(t *testing.T, base string) map[string]float64 {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, a scraper expects the text exposition format", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndex(line, " ")
		if i < 0 {
			t.Fatalf("unparseable sample line %q", line)
		}
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("unparseable value in %q: %v", line, err)
		}
		out[line[:i]] = v
	}
	return out
}

func TestMetricsExposesNodeHealth(t *testing.T) {
	srv, _, _ := testServer(t)
	m := scrape(t, srv.URL)

	if m["dedi_up"] != 1 {
		t.Errorf("dedi_up = %v, want 1 on a node with a reachable database", m["dedi_up"])
	}
	if _, ok := m["dedi_uptime_seconds"]; !ok {
		t.Error("dedi_uptime_seconds missing")
	}
	// A node that has never signed reports -1, not 0: a checkpoint signed this
	// instant and one that has never existed are different facts, and an alert
	// on age would read the second as the healthiest possible node.
	if got := m["dedi_checkpoint_age_seconds"]; got != -1 {
		t.Errorf("dedi_checkpoint_age_seconds = %v, want -1 before anything is signed", got)
	}
}

// TestMetricsExposesReplicaLag is task #30 itself: the lag was only ever on
// /dedi/network, which means catching a falling-behind replica required a human
// to have the page open.
func TestMetricsExposesReplicaLag(t *testing.T) {
	srv, _ := followerServer(t, cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower", LeaderID: "r0",
		Term: 4, CommitIndex: 12, AppliedIndex: 9, LagEntries: 3,
		LastContactSeconds: 90,
		Members:            []cluster.Member{{ID: "r0", Leader: true}, {ID: "r1", Self: true}},
	})
	m := scrape(t, srv.URL)

	want := map[string]float64{
		`dedi_cluster_enabled`:                            1,
		`dedi_cluster_is_leader{node_id="r1"}`:            0,
		`dedi_cluster_has_leader{node_id="r1"}`:           1,
		`dedi_cluster_term{node_id="r1"}`:                 4,
		`dedi_cluster_commit_index{node_id="r1"}`:         12,
		`dedi_cluster_applied_index{node_id="r1"}`:        9,
		`dedi_cluster_lag_entries{node_id="r1"}`:          3,
		`dedi_cluster_last_contact_seconds{node_id="r1"}`: 90,
		`dedi_cluster_members{node_id="r1"}`:              2,
	}
	for series, v := range want {
		got, ok := m[series]
		if !ok {
			t.Errorf("%s missing", series)
			continue
		}
		if got != v {
			t.Errorf("%s = %v, want %v", series, got, v)
		}
	}
}

// TestMetricsReportsZeroLagOnAnUnreplicatedNode: the series must exist even
// when there is nothing to replicate. An alert written as "lag > 0" is silently
// disarmed by an absent series, so absence would be the most dangerous possible
// way to say "healthy".
func TestMetricsReportsZeroLagOnAnUnreplicatedNode(t *testing.T) {
	srv, _, _ := testServer(t)
	m := scrape(t, srv.URL)
	if got, ok := m["dedi_cluster_enabled"]; !ok || got != 0 {
		t.Errorf("dedi_cluster_enabled = %v (present %v), want 0", got, ok)
	}
	if _, ok := m[`dedi_cluster_lag_entries{node_id=""}`]; ok {
		t.Error("an unreplicated node reported a replica lag it cannot have")
	}
}

// TestMetricsCountsRequestsButNotItsOwnScrape: /metrics is polled on a timer
// forever. Counting it would make requests_served climb steadily on a node
// nobody is using — the same reason the health probe and the dashboard's own
// polling are already excluded.
func TestMetricsCountsRequestsButNotItsOwnScrape(t *testing.T) {
	srv, _, _ := testServer(t)
	if _, err := http.Get(srv.URL + "/dedi/lookup/absent"); err != nil {
		t.Fatal(err)
	}
	before := scrape(t, srv.URL)
	scrape(t, srv.URL)
	after := scrape(t, srv.URL)

	const series = `dedi_requests_total{status_class="4xx"}`
	if before[series] < 1 {
		t.Fatalf("%s = %v, want the missed lookup counted", series, before[series])
	}
	if after[series] != before[series] {
		t.Errorf("scraping /metrics moved %s from %v to %v", series, before[series], after[series])
	}
}

// TestMetricsEscapesOperatorSuppliedLabels: peer names and node IDs come from
// configuration. An unescaped quote does not corrupt one series — it makes the
// whole document unparseable, taking every other metric down with it.
func TestMetricsEscapesOperatorSuppliedLabels(t *testing.T) {
	_, s, _ := testServer(t)
	mon := &network.Monitor{Peers: []network.Peer{{Name: `node "b"`, URL: "https://b.example"}}}
	mon.Observe(network.Status{Name: `node "b"`, URL: "https://b.example", Reachable: true})
	srv := httptest.NewServer((&Server{
		Store:   s,
		CP:      &checkpoint.Checkpointer{Store: s, Interval: time.Hour},
		Network: mon,
	}).Handler())
	t.Cleanup(srv.Close)

	m := scrape(t, srv.URL) // parses, or the test fails inside scrape
	if got := m[`dedi_peer_reachable{peer="node \"b\"",url="https://b.example"}`]; got != 1 {
		t.Errorf("escaped peer series missing or wrong: %v", m)
	}
}

// TestMetricsDeclaresEveryFamilyOnce: a repeated HELP/TYPE for one metric name
// is a document a strict parser rejects, and dedi_requests_total emits four
// samples from one family.
func TestMetricsDeclaresEveryFamilyOnce(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	seen := map[string]int{}
	re := regexp.MustCompile(`(?m)^# TYPE (\S+) `)
	for _, match := range re.FindAllStringSubmatch(string(body), -1) {
		seen[match[1]]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s declared %d times", name, n)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no metric families declared at all")
	}
}
