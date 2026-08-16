package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// notLeaderWriter stands in for the Raft proposer on a follower. What is under
// test here is the HTTP layer's reaction to ErrNotLeader; internal/cluster
// tests the replication itself against a real cluster, and running elections
// here would only add flakiness to something that does not depend on them.
type notLeaderWriter struct{}

func (notLeaderWriter) Append(context.Context, store.AppendInput) (store.Entry, error) {
	return store.Entry{}, cluster.ErrNotLeader
}

func followerServer(t *testing.T, state cluster.State) (*httptest.Server, ed25519.PrivateKey, *store.Store) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close() // wanted only for its store setup

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := publisher.ParseKeySet("op-1:ns:" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, err := note.GenerateKey(rand.Reader, "follower.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{
		Store:   s,
		CP:      &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "follower.test/log", Interval: time.Hour},
		TTL:     300,
		Writer:  notLeaderWriter{},
		Auth:    &publisher.Authenticator{Keys: keys},
		Cluster: func() cluster.State { return state },
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, priv, s
}

// signedPut builds a publisher-signed namespace write. The write plane is
// closed to anything unsigned, so a redirect test still has to sign.
func signedPut(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, path string, body []byte) *http.Request {
	t.Helper()
	pre := publisher.Precondition{IfNoneMatch: "*"}
	req, err := http.NewRequest(http.MethodPut, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	req.Header.Set("If-None-Match", pre.IfNoneMatch)
	req.Header.Set(publisher.HeaderKeyID, "op-1")
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature,
		publisher.Sign(priv, http.MethodPut, path, body, pre, now))
	return req
}

func TestAFollowerRedirectsWritesToTheLeader(t *testing.T) {
	srv, priv, _ := followerServer(t, cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower",
		LeaderID: "r0", LeaderURL: "https://leader.example",
	})

	// 307 rather than 308 or 302: the body is what the publisher signed, so the
	// method and body must survive the redirect, and leadership moves, so the
	// redirect must not be cacheable as permanent.
	req := signedPut(t, srv, priv, "/admin/namespaces/ns", []byte(`{"payload":{"description":"d"}}`))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want 307 — body: %s", resp.StatusCode, got)
	}
	if loc := resp.Header.Get("Location"); loc != "https://leader.example/admin/namespaces/ns" {
		t.Fatalf("Location %q", loc)
	}
}

func TestWithNoLeaderAFollowerSaysSoRatherThanFailing(t *testing.T) {
	// Mid-election. The node is healthy and the directory is readable; only the
	// write cannot proceed, and only for a moment, so it must not read as a
	// node fault.
	srv, priv, _ := followerServer(t, cluster.State{Enabled: true, NodeID: "r1", Role: "candidate"})

	resp, err := http.DefaultClient.Do(
		signedPut(t, srv, priv, "/admin/namespaces/ns", []byte(`{"payload":{"description":"d"}}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After: a client cannot tell this is transient")
	}
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad JSON: %s", b)
	}
	if m["code"] != "NO_LEADER" {
		t.Errorf("code %v, want NO_LEADER", m["code"])
	}
}

func TestReadsAreServedByAFollowerWithoutRedirect(t *testing.T) {
	srv, _, _ := followerServer(t, cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower",
		LeaderID: "r0", LeaderURL: "https://leader.example",
	})
	// The whole point of replication is that any replica answers reads. A
	// follower that redirected lookups would give away the availability the
	// cluster exists to provide.
	for _, path := range []string{"/dedi/network", "/dedi/log/checkpoint", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", path, resp.StatusCode)
		}
	}
}

func TestNetworkViewSeparatesClusterFromWitnessRing(t *testing.T) {
	srv, _, _ := followerServer(t, cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower",
		LeaderID: "r0", LeaderURL: "https://leader.example",
		CommitIndex: 12, AppliedIndex: 10, LagEntries: 2,
		Members: []cluster.Member{
			{ID: "r0", RaftAddr: "10.0.0.1:7000", HTTPURL: "https://leader.example", Leader: true},
			{ID: "r1", RaftAddr: "10.0.0.2:7000", Self: true},
		},
	})

	m := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)
	data := m["data"].(map[string]any)
	cl, ok := data["cluster"].(map[string]any)
	if !ok {
		t.Fatal("no cluster block in the network view")
	}
	if cl["enabled"] != true || cl["role"] != "follower" || cl["leader_id"] != "r0" {
		t.Fatalf("cluster block: %v", cl)
	}
	if cl["size"].(float64) != 2 {
		t.Errorf("size %v, want 2", cl["size"])
	}
	// Lag is published rather than folded into a pass/fail: a replica that is up
	// but permanently behind is exactly what a liveness check cannot see.
	if cl["lag_entries"].(float64) != 2 {
		t.Errorf("lag_entries %v, want 2", cl["lag_entries"])
	}

	// Cluster membership must never be mistaken for the witness ring. Replicas
	// of one node, run by one operator, prove nothing about each other; the ring
	// is the trust claim. They stay in separate objects so a reader cannot count
	// replicas as witnesses.
	first := data["nodes"].([]any)[0].(map[string]any)
	if _, leaked := first["leader"]; leaked {
		t.Error("cluster role leaked into the network node list")
	}
	if _, leaked := first["raft_addr"]; leaked {
		t.Error("raft membership leaked into the network node list")
	}
}

func TestAnUnreplicatedNodeReportsAClusterOfOne(t *testing.T) {
	srv, _, _ := testServer(t)
	m := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)
	cl := m["data"].(map[string]any)["cluster"].(map[string]any)
	// Reported explicitly rather than omitted: an absent field is
	// indistinguishable from a node too old to report one.
	if cl["enabled"] != false || cl["size"].(float64) != 1 {
		t.Fatalf("standalone node reports cluster %v", cl)
	}
}

// A follower must not answer a write from its own replica, even when the answer
// is only "nothing changed".
//
// The no-op shortcut reads the current version locally. On a follower that read
// can trail the leader, so a client republishing what this replica happens to
// hold — with a matching If-Match — used to be told 200 unchanged while the
// leader still had something else. Success is a claim about the log, and a
// follower is not the authority on it.
func TestAFollowerDoesNotAnswerANoOpFromItsOwnReplica(t *testing.T) {
	state := cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower",
		LeaderID: "r0", LeaderURL: "https://leader.example",
	}
	srv, priv, st := followerServer(t, state)

	// Put a record into this replica's local store directly, the way replication
	// would, without going through the (redirecting) write plane.
	seed := []store.AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "replica"},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "replica"},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "x",
			PayloadRaw: []byte(`{"a":1}`), State: "live", CreatedBy: "replica"},
	}
	for _, in := range seed {
		if _, err := st.Append(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}

	path := "/admin/namespaces/ns/registries/reg/records/x/publish"
	resp := signedNoFollow(t, srv, priv, path, []byte(`{"payload":{"a":1}}`))
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("follower answered a no-op locally: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 to the leader", resp.StatusCode)
	}
}

// Revoking something this replica already believes is revoked is the same
// claim, and must redirect for the same reason.
func TestAFollowerDoesNotAnswerAnAlreadyRevokedNoOpLocally(t *testing.T) {
	srv, priv, st := followerServer(t, cluster.State{
		Enabled: true, NodeID: "r1", Role: "follower",
		LeaderID: "r0", LeaderURL: "https://leader.example",
	})

	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "replica"},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "replica"},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "x",
			PayloadRaw: []byte(`{"a":1}`), State: "live", CreatedBy: "replica"},
		{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "x",
			PayloadRaw: []byte(`{"a":1}`), State: "revoked", CreatedBy: "replica"},
	} {
		if _, err := st.Append(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}

	path := "/admin/namespaces/ns/registries/reg/records/x/revoke"
	resp := signedNoFollow(t, srv, priv, path, []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 to the leader", resp.StatusCode)
	}
}

// signedNoFollow issues a signed write and stops at the redirect. Following it
// would leave the test asserting against leader.example rather than against
// what this replica decided.
func signedNoFollow(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, path string, body []byte) *http.Response {
	t.Helper()
	pre := currentPrecondition(t, srv, path)
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if pre.IfMatch != "" {
		req.Header.Set("If-Match", pre.IfMatch)
	}
	if pre.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", pre.IfNoneMatch)
	}
	now := time.Now().UTC()
	req.Header.Set(publisher.HeaderKeyID, "op-1")
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, http.MethodPost, path, body, pre, now))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
