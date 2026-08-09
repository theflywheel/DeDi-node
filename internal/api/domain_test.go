package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/domainproof"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// stubZone is a resolver holding a fixed set of TXT records, so these tests
// exercise the real verification path without depending on public DNS.
type stubZone map[string][]string

func (z stubZone) LookupTXT(_ context.Context, name string) ([]string, error) {
	txt, ok := z[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return txt, nil
}

const domainTestNodeKey = "test-node-verifier-key"

// domainServer boots a write-enabled node with a stub resolver attached.
func domainServer(t *testing.T, ns string, z stubZone) (*httptest.Server, *store.Store, ed25519.PrivateKey) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := publisher.ParseKeySet("op-1:" + ns + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, err := note.GenerateKey(rand.Reader, "domain.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "domain.test/log", Interval: time.Hour},
		TTL:   300, VerifierKey: domainTestNodeKey,
		WildcardNamespaces: []string{ns},
		Auth:               &publisher.Authenticator{Keys: keys},
		DNSResolver:        z,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, s, priv
}

func seedNamespaceWithDomain(t *testing.T, s *store.Store, ns, domain string) {
	t.Helper()
	payload := []byte(`{"description":"test","domain":"` + domain + `"}`)
	if _, err := s.Append(context.Background(), store.AppendInput{
		EntryType: "namespace", Namespace: ns, PayloadRaw: payload, CreatedBy: "seed",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDomainVerificationRoundTrip is the whole feature: the node tells the
// operator what to publish, refuses while it is absent, accepts once it is
// there, and records the binding where it can be read back.
func TestDomainVerificationRoundTrip(t *testing.T) {
	const ns, domain = "beckn-testnet", "example.org"
	z := stubZone{}
	srv, s, priv := domainServer(t, ns, z)
	seedNamespaceWithDomain(t, s, ns, domain)

	// 1. Ask what to publish.
	resp := signedDo(t, srv, priv, http.MethodGet, "/admin/namespaces/"+ns+"/domain", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET domain: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	data := bodyOf(t, resp)["data"].(map[string]any)
	if data["verified"] != false {
		t.Fatalf("a namespace with no TXT record must not read as verified: %v", data)
	}
	ch := data["challenge"].(map[string]any)
	name, value := ch["name"].(string), ch["value"].(string)
	if name != "_dedi-challenge."+domain {
		t.Fatalf("challenge name = %q", name)
	}

	// 2. Verifying before publishing is the caller's state, not a node fault.
	resp = signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/"+ns+"/domain/verify", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("verify before publishing: %d %v", resp.StatusCode, bodyOf(t, resp))
	}

	// 3. Publish exactly what we were told, and verify.
	z[name] = []string{"v=spf1 -all", value}
	resp = signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/"+ns+"/domain/verify", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify after publishing: %d %v", resp.StatusCode, bodyOf(t, resp))
	}

	// 4. The verdict is readable, and says which domain it was taken against.
	resp = signedDo(t, srv, priv, http.MethodGet, "/admin/namespaces/"+ns+"/domain", nil)
	data = bodyOf(t, resp)["data"].(map[string]any)
	if data["verified"] != true {
		t.Fatalf("verdict not recorded: %v", data)
	}
	if data["verified_for"] != domain {
		t.Fatalf("verified_for = %v, want %s", data["verified_for"], domain)
	}
	if data["verified_at"] == "" {
		t.Fatal("verified_at is empty")
	}

	// 5. Withdrawing it takes effect, without rewriting the log.
	resp = signedDo(t, srv, priv, http.MethodDelete, "/admin/namespaces/"+ns+"/domain", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unverify: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	resp = signedDo(t, srv, priv, http.MethodGet, "/admin/namespaces/"+ns+"/domain", nil)
	if bodyOf(t, resp)["data"].(map[string]any)["verified"] != false {
		t.Fatal("withdrawn verification still reads as verified")
	}
	// The history is still there — retraction is a new entry, not an erasure.
	versions, err := s.Versions(context.Background(), "record", domainProofNS, domainProofRegistry, ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions (verified, withdrawn), got %d", len(versions))
	}
}

// TestDomainVerificationRejectsAnotherNamespacesRecord is the property that
// makes this a proof rather than a formality. Publishing one namespace's token
// must not verify a second namespace on the same domain, or the first operator
// to verify would hand every later claimant a free pass.
func TestDomainVerificationRejectsAnotherNamespacesRecord(t *testing.T) {
	const domain = "shared.example"
	z := stubZone{}
	srv, s, priv := domainServer(t, "ns-a", z)
	seedNamespaceWithDomain(t, s, "ns-a", domain)

	// A token legitimately issued for a different namespace, published on the
	// same domain.
	name, otherToken := domainproof.Challenge("ns-b", domain, domainTestNodeKey)
	z[name] = []string{otherToken}

	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/ns-a/domain/verify", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("another namespace's token verified ns-a: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
}

// TestDomainVerificationNeedsADeclaredDomain: there is nothing to prove control
// of if the namespace never said what domain it claims.
func TestDomainVerificationNeedsADeclaredDomain(t *testing.T) {
	srv, s, priv := domainServer(t, "no-domain", stubZone{})
	if _, err := s.Append(context.Background(), store.AppendInput{
		EntryType: "namespace", Namespace: "no-domain", PayloadRaw: []byte(`{"description":"x"}`), CreatedBy: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/no-domain/domain/verify", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %v", resp.StatusCode, bodyOf(t, resp))
	}
}

// TestDomainBookkeepingStaysOffTheSpecSurface: the verdict lives in an
// underscore namespace, so it must not appear on the standard's read endpoints
// or in a published DeDi file. Adding a domain_verified field to the lookup
// response would be an extension the spec's schema does not declare.
func TestDomainBookkeepingStaysOffTheSpecSurface(t *testing.T) {
	const ns, domain = "beckn-testnet", "example.org"
	z := stubZone{}
	srv, s, priv := domainServer(t, ns, z)
	seedNamespaceWithDomain(t, s, ns, domain)
	name, value := domainproof.Challenge(ns, domain, domainTestNodeKey)
	z[name] = []string{value}
	if resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/"+ns+"/domain/verify", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("verify: %d %v", resp.StatusCode, bodyOf(t, resp))
	}

	resp, err := http.Get(srv.URL + "/dedi/lookup/" + domainProofNS)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("%s is visible on the spec read plane: %d", domainProofNS, resp.StatusCode)
	}

	m := getJSON(t, srv.URL+"/dedi/lookup/"+ns, http.StatusOK)
	for _, extra := range []string{"domain_verified", "verified", "verified_at"} {
		if _, present := m["data"].(map[string]any)[extra]; present {
			t.Errorf("lookup response carries %q, which openapi.yaml does not declare", extra)
		}
	}
}
