package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func seedBeckn(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{"description":"beckn testnet"}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{"description":"participants"}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-bap-1",
		PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","url":"http://sandbox-bap:3001","type":"BAP","domain":"retail","signing_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","encr_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","network_memberships":["beckn.one/testnet"]}`), CreatedBy: "seed"})
}

func TestBecknWildcardLookupBySubscriberID(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	// ONIX Lookup shape: /dedi/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}
	m := getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-bap-1", http.StatusOK)
	data := m["data"].(map[string]any)
	details := data["details"].(map[string]any)
	if details["signing_public_key"] != "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=" || details["subscriber_id"] != "bap.example.com" {
		t.Fatalf("details: %v", details)
	}
	nm := data["network_memberships"].([]any)
	if len(nm) != 1 || nm[0] != "beckn.one/testnet" {
		t.Fatalf("network_memberships: %v", data["network_memberships"])
	}
	if _, hasTTL := data["ttl"].(float64); !hasTTL {
		t.Fatalf("ttl missing: %v", data["ttl"])
	}
}

func TestBecknWildcardLookupExactStillWins(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	// LookupNode shape: real namespace + literal registry name = plain exact lookup
	m := getJSON(t, srv.URL+"/dedi/lookup/beckn-testnet/subscribers.beckn.one/key-bap-1", http.StatusOK)
	if m["data"].(map[string]any)["record_name"] != "key-bap-1" {
		t.Fatalf("exact lookup broken: %v", m["data"])
	}
}

func TestBecknWildcardLookupUnknown404(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/no-such-key", http.StatusNotFound)
	if m["code"] != "NOT_FOUND" {
		t.Fatalf("code: %v", m["code"])
	}
	// wildcard fallback must NOT trigger for ordinary registry names
	getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/participants/key-bap-1", http.StatusNotFound)
}

func TestBecknWildcardLookupWithProof(t *testing.T) {
	srv, s, vkey := testServer(t)
	seedBeckn(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-bap-1?proof=inclusion", http.StatusOK)
	proof, ok := m["proof"].(map[string]any)
	if !ok {
		t.Fatalf("proof missing on wildcard hit: %v", m)
	}
	leaf := proof["leaf"].(map[string]any)
	// proof leaf must carry the FOUND record's real identity, not the request path
	if leaf["namespace"] != "beckn-testnet" || leaf["registry"] != "subscribers.beckn.one" || leaf["record_name"] != "key-bap-1" {
		t.Fatalf("proof leaf identity: %v", leaf)
	}
	if proof["checkpoint"].(string) == "" || len(proof["path"].([]any)) == 0 && proof["tree_size"].(float64) > 1 {
		t.Fatalf("degenerate proof: %v", proof)
	}
	_ = vkey // full offline verification is covered by e2e_test.go; this test pins presence + identity on the wildcard path
}

func TestBecknRecordMetaAndDescriptionHoisted(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	if _, err := s.Append(context.Background(), store.AppendInput{
		EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "bpp.node.example.com",
		PayloadRaw: []byte(`{"subscriber_id":"bpp.node.example.com","description":"node record","meta":{"manifestUrl":"http://x/manifest.json"}}`),
		CreatedBy:  "seed"}); err != nil {
		t.Fatal(err)
	}
	m := getJSON(t, srv.URL+"/dedi/lookup/beckn-testnet/subscribers.beckn.one/bpp.node.example.com", http.StatusOK)
	data := m["data"].(map[string]any)
	meta, ok := data["meta"].(map[string]any)
	if !ok || meta["manifestUrl"] != "http://x/manifest.json" {
		t.Fatalf("meta not hoisted to data.meta: %v", data["meta"])
	}
	if data["description"] != "node record" {
		t.Fatalf("description not hoisted: %v", data["description"])
	}
	// record without meta still yields empty object, not missing key
	m = getJSON(t, srv.URL+"/dedi/lookup/beckn-testnet/subscribers.beckn.one/key-bap-1", http.StatusOK)
	if _, ok := m["data"].(map[string]any)["meta"].(map[string]any); !ok {
		t.Fatalf("empty meta must still be an object: %v", m["data"])
	}
}

// End of the escalation path over HTTP: a rogue namespace publishing a record
// under someone else's subscriber_id must not answer the ONIX wildcard lookup
// once the node restricts eligibility (design.md:256).
func TestBecknWildcardHonoursEligibleNamespaces(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	ctx := context.Background()
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "rogue-net", PayloadRaw: []byte(`{}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "registry", Namespace: "rogue-net", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "record", Namespace: "rogue-net", Registry: "subscribers.beckn.one", RecordName: "key-rogue",
		PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","signing_public_key":"attacker","type":"BAP"}`), CreatedBy: "seed"})

	// Unrestricted node (today's read-only default): the rogue record answers.
	getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-rogue", http.StatusOK)

	// Restricted node: it does not, while the real participant still resolves.
	restricted := httptest.NewServer((&Server{
		Store: s, CP: nil, TTL: 300, WildcardNamespaces: []string{"beckn-testnet"},
	}).Handler())
	defer restricted.Close()

	getJSON(t, restricted.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-rogue", http.StatusNotFound)
	m := getJSON(t, restricted.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-bap-1", http.StatusOK)
	details := m["data"].(map[string]any)["details"].(map[string]any)
	if details["signing_public_key"] == "attacker" {
		t.Fatal("attacker key served from an ineligible namespace")
	}
}
