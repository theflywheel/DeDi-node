package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func TestHealthzReportsReachableDatabase(t *testing.T) {
	srv, _, _ := testServer(t)
	m := getJSON(t, srv.URL+"/healthz", http.StatusOK)
	if m["status"] != "ok" {
		t.Fatalf("status = %v, want ok", m["status"])
	}
	if m["database"] != "ok" {
		t.Fatalf("database = %v, want ok", m["database"])
	}
	if _, present := m["uptime_seconds"]; !present {
		t.Error("uptime_seconds missing")
	}
	// An empty log has published nothing yet, and that is not a failure.
	if m["checkpoint"] != "none published" {
		t.Errorf("checkpoint = %v, want \"none published\"", m["checkpoint"])
	}
}

// A quiet registry is a healthy registry. The checkpointer writes no new row
// while the tree is unchanged, so gating health on checkpoint age would report
// an outage whenever nobody published — the age is informational only.
func TestHealthzReportsLogStateWithoutGatingOnIt(t *testing.T) {
	ctx := context.Background()
	srv, s, _ := testServer(t)
	if _, err := s.Append(ctx, store.AppendInput{
		EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}
	// Publishing the checkpoint is what /dedi/log/checkpoint does on first read.
	if _, err := http.Get(srv.URL + "/dedi/log/checkpoint"); err != nil {
		t.Fatal(err)
	}

	m := getJSON(t, srv.URL+"/healthz", http.StatusOK)
	if m["status"] != "ok" {
		t.Fatalf("status = %v, want ok", m["status"])
	}
	if got, want := m["tree_size"], float64(1); got != want {
		t.Errorf("tree_size = %v, want %v", got, want)
	}
	age, present := m["checkpoint_age_seconds"].(float64)
	if !present {
		t.Fatalf("checkpoint_age_seconds missing or not a number: %v", m["checkpoint_age_seconds"])
	}
	if age < 0 {
		t.Errorf("checkpoint_age_seconds = %v, must not be negative", age)
	}
}

// The probe runs on a monitor's schedule, not a user's. Counting it would make
// the served-request total climb on its own.
func TestHealthzExcludedFromRequestCount(t *testing.T) {
	srv, _, _ := testServer(t)
	for range 3 {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := servedCount(t, srv.URL); got != 0 {
		t.Fatalf("requests_served = %v, want 0", got)
	}
}

// A node that cannot reach its database must fail the probe, not pass it — that
// is the entire failure mode this endpoint exists to catch. Closing the pool is
// the closest reproduction of a database that has gone away.
func TestHealthzFailsWhenDatabaseUnreachable(t *testing.T) {
	srv, s, _ := testServer(t)
	s.Close()
	m := getJSON(t, srv.URL+"/healthz", http.StatusServiceUnavailable)
	if m["status"] != "unavailable" {
		t.Errorf("status = %v, want unavailable", m["status"])
	}
	if m["database"] != "unreachable" {
		t.Errorf("database = %v, want unreachable", m["database"])
	}
}
