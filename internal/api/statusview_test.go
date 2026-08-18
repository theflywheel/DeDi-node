package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// A checkpoint gap means one of two very different things, and the endpoints
// exist so a reader can tell them apart. This asserts both series are actually
// servable from the same window — without the activity series, the signing
// history invites exactly the wrong reading, which is what our own Prometheus
// rule does today.
// Both series must arrive on ONE grid, from one response, or the page has to
// line up two grids and will get it wrong — which is exactly what happened.
func TestHistoryServesBothSeriesOnOneGrid(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	seedBasic(t, s)
	if err := s.SaveCheckpoint(ctx, 3, make([]byte, 32), "note"); err != nil {
		t.Fatal(err)
	}

	m := getJSON(t, srv.URL+"/dedi/log/history?buckets=6&bucket_seconds=3600", http.StatusOK)
	d := m["data"].(map[string]any)
	bs := d["buckets"].([]any)
	if len(bs) != 6 {
		t.Fatalf("got %d buckets, want the 6 requested", len(bs))
	}
	first := bs[0].(map[string]any)
	for _, k := range []string{"at", "checkpoints", "entries"} {
		if _, ok := first[k]; !ok {
			t.Errorf("bucket is missing %q", k)
		}
	}
	// The field that makes a fault decidable without comparing clocks.
	if _, ok := d["signed_through"]; !ok {
		t.Error("no signed_through, so 'unsigned' would have to be guessed from bucket timing")
	}
	if d["entries"].(float64) < 1 {
		t.Error("no entries reported, so a signing gap could not be attributed")
	}
}

// On a Raft follower this node signs nothing: every checkpoint is the leader's,
// applied locally. The page must be told, or it says "what this node signed" on
// two of every three replicas, which is false.
func TestHistorySaysWhetherThisNodeSigns(t *testing.T) {
	srv, _, _ := testServer(t)
	m := getJSON(t, srv.URL+"/dedi/log/history", http.StatusOK)
	d := m["data"].(map[string]any)
	if d["signs_here"] != true {
		t.Errorf("an unreplicated node reports signs_here=%v, want true", d["signs_here"])
	}
	if d["cluster_enabled"] != false {
		t.Errorf("cluster_enabled=%v on a standalone node", d["cluster_enabled"])
	}
}

// The bucket count is caller-supplied and must stay bounded.
func TestHistoryIsBoundedOverTheWire(t *testing.T) {
	srv, _, _ := testServer(t)
	m := getJSON(t, srv.URL+"/dedi/log/history?buckets=100000", http.StatusOK)
	if n := len(m["data"].(map[string]any)["buckets"].([]any)); n > 500 {
		t.Errorf("served %d buckets from an absurd request", n)
	}
}

// The page's thesis is that it reports signing, not availability. If it ever
// starts claiming uptime, the claim is false — the node holds no record of its
// own past processes at all.
func TestStatusPageDoesNotClaimUptime(t *testing.T) {
	page := string(statusPageHTML)
	if !strings.Contains(page, "record of signing, not of availability") {
		t.Error("the status page no longer states what it is a record of")
	}
	// A hardcoded third-party monitor would have every deployment of this
	// binary assert that something it does not run is watching it.
	if strings.Contains(page, "status.beckn.try-dough.com") {
		t.Error("the status page hardcodes one deployment's external monitor")
	}
	for _, lie := range []string{"% uptime", "uptime over", "continuous operation", "no outages"} {
		if strings.Contains(strings.ToLower(page), lie) {
			t.Errorf("the status page claims %q, which the node cannot know", lie)
		}
	}
	// The three states must all be named; collapsing idle into fault is the
	// bug this page exists to avoid repeating.
	for _, state := range []string{"idle", "no signature covers", "does not sign"} {
		if !strings.Contains(strings.ToLower(page), state) {
			t.Errorf("the status page never mentions the %q state", state)
		}
	}
}

var _ = store.HistoryBucket{}
