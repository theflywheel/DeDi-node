package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// networkServer builds a node that knows about the given peer URLs.
func networkServer(t *testing.T, peers []network.Peer) (*httptest.Server, *store.Store) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	skey, _, err := note.GenerateKey(rand.Reader, "self.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "self.dedi.local/log", Interval: time.Hour}

	var mon *network.Monitor
	if len(peers) > 0 {
		mon = &network.Monitor{Peers: peers, Interval: time.Hour}
		// Poll once synchronously so the assertion reads a real observation
		// rather than racing the background loop.
		go mon.Run(ctx)
		time.Sleep(300 * time.Millisecond)
	}
	srv := httptest.NewServer((&Server{
		Store: s, CP: cp, TTL: 300, NodeName: "node-a", Network: mon,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func TestNetworkViewOfAStandaloneNodeIsANetworkOfOne(t *testing.T) {
	srv, _ := networkServer(t, nil)
	data := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)["data"].(map[string]any)

	// A node with no peers has a network of exactly itself. Reporting zero
	// would be wrong: the node reading the page is on the network.
	if total := data["total"].(float64); total != 1 {
		t.Fatalf("total %v, want 1", total)
	}
	if reachable := data["reachable"].(float64); reachable != 1 {
		t.Fatalf("reachable %v, want 1", reachable)
	}
	self := data["nodes"].([]any)[0].(map[string]any)
	if self["self"] != true || self["name"] != "node-a" {
		t.Fatalf("unexpected self node: %+v", self)
	}
}

func TestNetworkViewReportsAReachablePeerAndItsTreeSize(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "peer.dedi.local/log\n7\nZm9vCg==\n\n— peer.dedi.local/log sig\n")
	}))
	defer peer.Close()

	srv, _ := networkServer(t, []network.Peer{{Name: "node-b", URL: peer.URL}})
	data := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)["data"].(map[string]any)

	if total := data["total"].(float64); total != 2 {
		t.Fatalf("total %v, want 2", total)
	}
	if reachable := data["reachable"].(float64); reachable != 2 {
		t.Fatalf("reachable %v, want 2", reachable)
	}
	got := data["nodes"].([]any)[1].(map[string]any)
	if got["name"] != "node-b" || got["reachable"] != true {
		t.Fatalf("unexpected peer: %+v", got)
	}
	if size := got["tree_size"].(float64); size != 7 {
		t.Errorf("peer tree_size %v, want 7", size)
	}
}

func TestNetworkViewCountsAnUnreachablePeerAsUnreachable(t *testing.T) {
	// The count is the number the page leads with, so a down peer must reduce
	// it. A view that always reports every node reachable is worse than none.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	srv, _ := networkServer(t, []network.Peer{{Name: "node-b", URL: url}})
	data := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)["data"].(map[string]any)

	if total := data["total"].(float64); total != 2 {
		t.Fatalf("total %v, want 2", total)
	}
	if reachable := data["reachable"].(float64); reachable != 1 {
		t.Fatalf("reachable %v, want 1 — only this node is answering", reachable)
	}
	got := data["nodes"].([]any)[1].(map[string]any)
	if got["reachable"] != false {
		t.Fatalf("peer should be unreachable: %+v", got)
	}
	if _, reported := got["tree_size"]; reported {
		t.Error("an unreachable peer must not report a tree size")
	}
}

func TestNetworkViewIsExcludedFromTheRequestCount(t *testing.T) {
	// The explorer polls this on a timer; counting it would make the node's own
	// served-request total climb with nobody using it.
	if !selfTraffic("/dedi/network") {
		t.Fatal("/dedi/network should not count as served traffic")
	}
}
