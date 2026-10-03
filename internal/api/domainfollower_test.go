package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/domainproof"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"golang.org/x/mod/sumdb/note"
	"net/http/httptest"
)

// Domain verification on a follower sends the caller to the leader (#76).
//
// It is the one write that creates its own parents first, through the log, on
// a replica that has never seen them. On a follower that append is refused as
// not-leader, and it was answered 500, so the very first verification on a
// fresh cluster failed whenever the load balancer picked a follower.
func TestDomainVerificationOnAFollowerRedirectsToTheLeader(t *testing.T) {
	const ns, domain, leader = "beckn-testnet", "example.org", "https://leader.example"
	base, s, _ := testServer(t)
	base.Close()
	seedNamespaceWithDomain(t, s, ns, domain)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := publisher.ParseKeySet("op-1:" + ns + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, _ := note.GenerateKey(rand.Reader, "domain.follower.test")
	name, value := domainproof.Challenge(ns, domain, domainTestNodeKey)
	srv := httptest.NewServer((&Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "domain.follower.test/log", Interval: time.Hour},
		TTL: 300, VerifierKey: domainTestNodeKey, WildcardNamespaces: []string{ns},
		Auth:        &publisher.Authenticator{Keys: keys},
		DNSResolver: stubZone{name: {value}}, // the operator has published the proof
		Writer:      notLeaderWriter{},
		Cluster:     func() cluster.State { return cluster.State{Role: "follower", LeaderURL: leader} },
	}).Handler())
	t.Cleanup(srv.Close)

	// signedDo uses the default client, which would follow the 307 to a host that
	// does not exist; stop at the redirect to read it.
	http.DefaultClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(func() { http.DefaultClient.CheckRedirect = nil })
	path := "/admin/namespaces/" + ns + "/domain/verify"
	resp := signedDo(t, srv, priv, http.MethodPost, path, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("verify on a follower: %d, want 307 to the leader", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != leader+path {
		t.Errorf("redirected to %q, want %q", loc, leader+path)
	}
}
