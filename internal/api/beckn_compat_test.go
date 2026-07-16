package api

import (
	"context"
	"net/http"
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
