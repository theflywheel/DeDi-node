package network

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParsePeersAcceptsBothForms(t *testing.T) {
	peers, err := ParsePeers("node-b=https://b.example.org, https://c.example.org/ ,")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("want 2 peers, got %d: %+v", len(peers), peers)
	}
	if peers[0] != (Peer{Name: "node-b", URL: "https://b.example.org"}) {
		t.Errorf("named peer: %+v", peers[0])
	}
	// An unnamed peer falls back to its host, not the whole URL, and the
	// trailing slash is stripped so paths are appended cleanly.
	if peers[1] != (Peer{Name: "c.example.org", URL: "https://c.example.org"}) {
		t.Errorf("unnamed peer: %+v", peers[1])
	}
}

func TestParsePeersRejectsThingsThatAreNotURLs(t *testing.T) {
	for _, spec := range []string{"b.example.org", "node-b=b.example.org", "ftp://b.example.org"} {
		if _, err := ParsePeers(spec); err == nil {
			t.Errorf("%q: want an error", spec)
		}
	}
}

func TestParsePeersTreatsEmptyAsNoPeers(t *testing.T) {
	// A standalone node is a supported deployment mode, not a misconfiguration.
	peers, err := ParsePeers("")
	if err != nil || len(peers) != 0 {
		t.Fatalf("want no peers and no error, got %+v / %v", peers, err)
	}
}

func checkpointServer(t *testing.T, body string, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dedi/log/checkpoint" {
			t.Errorf("polled %s, want /dedi/log/checkpoint", r.URL.Path)
		}
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPollReadsTheTreeSizeFromTheCheckpoint(t *testing.T) {
	srv := checkpointServer(t, "b.example.org/log\n42\nZm9vCg==\n\n— b.example.org/log sig\n", 200)
	m := &Monitor{Peers: []Peer{{Name: "b", URL: srv.URL}}}
	m.pollAll(context.Background())

	got := m.Snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if !got[0].Reachable {
		t.Fatalf("want reachable, got error %q", got[0].Error)
	}
	if got[0].TreeSize != 42 {
		t.Errorf("tree size %d, want 42", got[0].TreeSize)
	}
	if got[0].Origin != "b.example.org/log" {
		t.Errorf("origin %q", got[0].Origin)
	}
}

func TestPollReportsFailuresRatherThanGuessing(t *testing.T) {
	cases := []struct {
		name, body string
		code       int
	}{
		{"server error", "boom", 500},
		{"not a checkpoint", "hello", 200},
		{"size is not a number", "b.example.org/log\nmany\nhash\n", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := checkpointServer(t, tc.body, tc.code)
			m := &Monitor{Peers: []Peer{{Name: "b", URL: srv.URL}}}
			m.pollAll(context.Background())
			got := m.Snapshot()[0]
			if got.Reachable {
				t.Fatal("want unreachable: a peer that cannot serve its log is not serving")
			}
			if got.Error == "" {
				t.Error("want an explanation, got none")
			}
			if got.TreeSize != 0 {
				t.Errorf("tree size %d reported despite a failed poll", got.TreeSize)
			}
		})
	}
}

func TestPollMarksADeadPeerUnreachable(t *testing.T) {
	srv := checkpointServer(t, "", 200)
	url := srv.URL
	srv.Close()

	m := &Monitor{Peers: []Peer{{Name: "gone", URL: url}},
		Client: &http.Client{Timeout: 2 * time.Second}}
	m.pollAll(context.Background())
	if got := m.Snapshot()[0]; got.Reachable {
		t.Fatal("a peer that refuses connections must not read as reachable")
	}
}

func TestSnapshotListsPeersNotYetPolled(t *testing.T) {
	// Before the first poll the honest answer is "not known", not "down with an
	// error we invented".
	m := &Monitor{Peers: []Peer{{Name: "b", URL: "https://b.example.org"}}}
	got := m.Snapshot()
	if len(got) != 1 || got[0].Name != "b" || got[0].Reachable || got[0].Error != "" {
		t.Fatalf("unexpected pre-poll snapshot: %+v", got)
	}
}
