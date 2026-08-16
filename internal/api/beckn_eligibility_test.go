package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// twoNamespaceServer opens the write plane for writeNS while declaring only
// eligibleNS authoritative for beckn subscriber identity. That split is the
// whole point: on a real node the operator decides who may answer as a beckn
// registry, and a publisher's own namespace scope does not grant it.
func twoNamespaceServer(t *testing.T, writeNS, eligibleNS string) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := publisher.ParseKeySet("op-1:" + writeNS + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, err := note.GenerateKey(rand.Reader, "eligibility.test")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "eligibility.test/log", Interval: time.Hour}
	srv := httptest.NewServer((&Server{
		Store: s, CP: cp, TTL: 300,
		WildcardNamespaces: []string{eligibleNS},
		Auth:               &publisher.Authenticator{Keys: keys},
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, priv
}

// A publisher scoped to a namespace it does control must not be able to answer
// ONIX identity lookups for a subscriber it does not hold, merely by naming its
// namespace after that subscriber and creating the matching record.
//
// ONIX reads any 200 on this path as a live participant — it checks neither
// state nor status — so serving the record is serving the identity.
func TestAnIneligibleNamespaceCannotAnswerAnONIXIdentityLookup(t *testing.T) {
	srv, priv := twoNamespaceServer(t, "bpp.acme.example", "beckn-testnet")

	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/bpp.acme.example",
		[]byte(`{"payload":{"description":"a namespace named after someone else's subscriber id"}}`))
	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/bpp.acme.example/registries/subscribers.beckn.one",
		[]byte(`{"payload":{"description":"participants"}}`))
	mustWrite(t, srv, priv, "POST",
		"/admin/namespaces/bpp.acme.example/registries/subscribers.beckn.one/records/KEY-1/publish",
		[]byte(`{"payload":{"subscriber_id":"bpp.acme.example","type":"BPP","url":"https://attacker.example/beckn",`+
			`"signing_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","network_memberships":["beckn.one/testnet"]}}`))

	// The exact three-part path ONIX's LookupNode uses.
	resp, err := http.Get(srv.URL + "/dedi/lookup/bpp.acme.example/subscribers.beckn.one/KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("ineligible namespace answered an identity lookup: %d %v", resp.StatusCode, body)
	}
}

// The record is still there, and reading it as a record rather than as a beckn
// identity claim is fine. Eligibility scopes who may answer *as a registry*, not
// what the node will admit exists.
func TestTheRecordItselfIsStillReadableUnderItsOwnRegistry(t *testing.T) {
	srv, priv := twoNamespaceServer(t, "bpp.acme.example", "beckn-testnet")

	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/bpp.acme.example", []byte(`{"payload":{}}`))
	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/bpp.acme.example/registries/other.registry",
		[]byte(`{"payload":{}}`))
	mustWrite(t, srv, priv, "POST",
		"/admin/namespaces/bpp.acme.example/registries/other.registry/records/KEY-1/publish",
		[]byte(`{"payload":{"subscriber_id":"bpp.acme.example"}}`))

	resp, err := http.Get(srv.URL + "/dedi/lookup/bpp.acme.example/other.registry/KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a record outside the beckn registry was gated: %d", resp.StatusCode)
	}
}

// An eligible namespace answers exactly as before. The gate must not cost the
// operator's own registry its exact-hit path.
func TestAnEligibleNamespaceStillAnswersTheExactPath(t *testing.T) {
	srv, priv := twoNamespaceServer(t, "beckn-testnet", "beckn-testnet")

	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet", []byte(`{"payload":{}}`))
	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one",
		[]byte(`{"payload":{}}`))
	mustWrite(t, srv, priv, "POST",
		"/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one/records/KEY-1/publish",
		[]byte(`{"payload":{"subscriber_id":"bpp.acme.example","type":"BPP","url":"https://bpp.acme.example/beckn",`+
			`"signing_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo="}}`))

	resp, err := http.Get(srv.URL + "/dedi/lookup/beckn-testnet/subscribers.beckn.one/KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("eligible namespace lost its exact hit: %d", resp.StatusCode)
	}
}

// A node with no allowlist configured — the shape it has when the write plane
// is closed — restricts nothing. There is no publisher to scope.
func TestNoAllowlistRestrictsNothing(t *testing.T) {
	srv, s, _ := testServer(t)
	if _, err := s.Append(t.Context(), store.AppendInput{
		EntryType: "namespace", Namespace: "anyone.example", PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), store.AppendInput{
		EntryType: "registry", Namespace: "anyone.example", Registry: "subscribers.beckn.one",
		PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), store.AppendInput{
		EntryType: "record", Namespace: "anyone.example", Registry: "subscribers.beckn.one",
		RecordName: "KEY-1", PayloadRaw: []byte(`{"subscriber_id":"anyone.example"}`),
		State: "live", CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/dedi/lookup/anyone.example/subscribers.beckn.one/KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a node with no allowlist gated a lookup: %d", resp.StatusCode)
	}
}

func mustWrite(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, method, path string, body []byte) {
	t.Helper()
	resp := signedDo(t, srv, priv, method, path, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, bodyOf(t, resp))
	}
}
