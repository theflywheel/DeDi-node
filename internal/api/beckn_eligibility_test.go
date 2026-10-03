package api

import (
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
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// twoNamespaceServer opens the write plane for writeNS while declaring only
// eligibleNS authoritative for beckn subscriber identity. That split is the
// whole point: on a real node the operator decides who may answer as a beckn
// registry, and a publisher's own namespace scope does not grant it.
func twoNamespaceServer(t *testing.T, writeNS, eligibleNS string) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	return eligibilityServer(t, writeNS, []string{eligibleNS})
}

// eligibilityServer opens the write plane for writeNS with the given allowlist.
func eligibilityServer(t *testing.T, writeNS string, eligible []string) (*httptest.Server, ed25519.PrivateKey) {
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
		WildcardNamespaces: eligible,
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

	// The exact three-part path ONIX's LookupNode uses, and the same question
	// asked "as on" today or later: that is still a question about now, not
	// history (#69), so it gets the same answer.
	for _, q := range []string{"", "?as_on=" + time.Now().UTC().Format(time.DateOnly), "?as_on=2100-01-01T00:00:00Z"} {
		resp, err := http.Get(srv.URL + "/dedi/lookup/bpp.acme.example/subscribers.beckn.one/KEY-1" + q)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound {
			var body map[string]any
			json.NewDecoder(resp.Body).Decode(&body)
			t.Errorf("ineligible namespace answered an identity lookup%s: %d %v", q, resp.StatusCode, body)
		}
		resp.Body.Close()
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

// A node with publisher keys and DEDI_WILDCARD_NAMESPACES unset boots with an
// empty allowlist (writePlaneConfig, #80). Nothing may answer the Beckn
// wildcard then, not even the subscriber's own namespace, and ?domain=
// discovery lists nobody; ordinary lookups are unaffected.
func TestAnEmptyAllowlistLeavesNothingEligible(t *testing.T) {
	srv, priv := eligibilityServer(t, "crest", []string{})

	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/crest", []byte(`{"payload":{}}`))
	for _, reg := range []string{"subscribers.beckn.one", "other.registry"} {
		mustWrite(t, srv, priv, "PUT", "/admin/namespaces/crest/registries/"+reg, []byte(`{"payload":{}}`))
	}
	mustWrite(t, srv, priv, "POST",
		"/admin/namespaces/crest/registries/subscribers.beckn.one/records/KEY-1/publish",
		[]byte(`{"payload":{"subscriber_id":"bpp.crest.example","type":"BPP","url":"https://bpp.crest.example/beckn"}}`))
	mustWrite(t, srv, priv, "POST",
		"/admin/namespaces/crest/registries/other.registry/records/KEY-1/publish",
		[]byte(`{"payload":{"subscriber_id":"bpp.crest.example","domain":"retail"}}`))

	for path, want := range map[string]int{
		// The exact hit in the record's own namespace, and the wildcard by
		// subscriber_id.
		"/dedi/lookup/crest/subscribers.beckn.one/KEY-1":             http.StatusNotFound,
		"/dedi/lookup/bpp.crest.example/subscribers.beckn.one/KEY-1": http.StatusNotFound,
		"/dedi/lookup/crest/other.registry/KEY-1":                    http.StatusOK,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	if _, out := discover(t, srv, "/dedi/query/crest/other.registry?domain=retail"); out.Data.Total != 0 {
		t.Errorf("?domain= discovery listed %d participants with nothing eligible", out.Data.Total)
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

// The exact path honours subscriber status the way the wildcard does (#89).
//
// ONIX treats any 200 as a usable participant, so an UNSUBSCRIBED exact hit in
// an eligible namespace was still routed to and had its signatures accepted.
// The status reaches a record through the console's onboarding form, a seed
// file or a direct write; revocation was already gated separately.
func TestAnUnsubscribedExactHitDoesNotResolve(t *testing.T) {
	srv, priv := twoNamespaceServer(t, "beckn-testnet", "beckn-testnet")
	const path = "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one/records/KEY-1/publish"
	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet", []byte(`{"payload":{}}`))
	mustWrite(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one",
		[]byte(`{"payload":{}}`))

	for _, c := range []struct {
		status string
		want   int
	}{
		{`"UNSUBSCRIBED"`, http.StatusNotFound},
		{`"INITIATED"`, http.StatusNotFound},
		{`"SUBSCRIBED"`, http.StatusOK},
		{`null`, http.StatusOK},
		// Only the exact key counts, as in payload->>'status'.
		{`"SUBSCRIBED","Status":"UNSUBSCRIBED"`, http.StatusOK},
		{`"UNSUBSCRIBED"`, http.StatusNotFound},
	} {
		mustWrite(t, srv, priv, "POST", path,
			[]byte(`{"payload":{"subscriber_id":"bpp.acme.example","url":"https://bpp.acme.example/beckn","status":`+c.status+`}}`))
		resp, err := http.Get(srv.URL + "/dedi/lookup/beckn-testnet/subscribers.beckn.one/KEY-1")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("status %s: exact lookup answered %d, want %d", c.status, resp.StatusCode, c.want)
		}
	}

	// Reading history is not an identity claim, so a pinned version still
	// answers; nor is the operator's include_revoked read, which the console
	// uses to edit or re-subscribe the participant.
	// version_id is a log seq: 3 is the first record write, UNSUBSCRIBED.
	for _, q := range []string{"?version_id=3", "?include_revoked=true"} {
		resp, err := http.Get(srv.URL + "/dedi/lookup/beckn-testnet/subscribers.beckn.one/KEY-1" + q)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("%s of an unsubscribed participant: %d, want 200 %s", q, resp.StatusCode, b)
		}
	}
}
