package api

import (
	"context"
	"net/http"
	"testing"
)

func servedCount(t *testing.T, url string) float64 {
	t.Helper()
	m := getJSON(t, url+"/dedi/stats", http.StatusOK)
	data, okData := m["data"].(map[string]any)
	if !okData {
		t.Fatalf("stats: no data object: %v", m)
	}
	n, okNum := data["requests_served"].(float64)
	if !okNum {
		t.Fatalf("stats: requests_served not a number: %v", data["requests_served"])
	}
	return n
}

func TestStatsCountsServedRequests(t *testing.T) {
	srv, _, _ := testServer(t)
	before := servedCount(t, srv.URL)

	getJSON(t, srv.URL+"/dedi/query/nope", http.StatusNotFound) // counts as 4xx
	resp, err := http.Get(srv.URL + "/dedi/log/checkpoint")     // counts as 2xx
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := servedCount(t, srv.URL); got != before+2 {
		t.Fatalf("requests_served = %v, want %v", got, before+2)
	}
}

// The explorer page polls /dedi/stats on a timer; counting that traffic would
// make the number climb on its own.
func TestStatsExcludesDashboardTraffic(t *testing.T) {
	srv, _, _ := testServer(t)
	for _, path := range []string{"/", "/docs", "/dedi/stats"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := servedCount(t, srv.URL); got != 0 {
		t.Fatalf("requests_served = %v, want 0", got)
	}
}

func TestStatsSurvivesFlush(t *testing.T) {
	ctx := context.Background()
	srv, store, _ := testServer(t)
	getJSON(t, srv.URL+"/dedi/query/nope", http.StatusNotFound)

	// Reach into the same Server the test HTTP server is using is not possible
	// here, so verify the store half directly: a flush is additive and readable.
	if err := store.AddRequestCounts(ctx, map[string]int64{"2xx": 7, "4xx": 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRequestCounts(ctx, map[string]int64{"2xx": 3}); err != nil {
		t.Fatal(err)
	}
	counts, err := store.RequestCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["2xx"] != 10 {
		t.Fatalf("persisted 2xx = %d, want 10 (adds must accumulate, not overwrite)", counts["2xx"])
	}
	if _, present := counts["4xx"]; present {
		t.Fatalf("zero delta should not create a row: %v", counts)
	}
	// The endpoint reports persisted + in-memory.
	if got := servedCount(t, srv.URL); got != 11 {
		t.Fatalf("requests_served = %v, want 11 (10 persisted + 1 in memory)", got)
	}
}
