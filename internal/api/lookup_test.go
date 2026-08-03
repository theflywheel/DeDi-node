package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func seedBasic(t *testing.T, s *store.Store) (nsE, regE, rec1, rec2 store.Entry) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) store.Entry {
		e, err := s.Append(ctx, in)
		if err != nil {
			t.Fatalf("seed append: %v", err)
		}
		return e
	}
	nsE = must(store.AppendInput{EntryType: "namespace", Namespace: "flywheel", PayloadRaw: []byte(`{"description":"Flywheel network","domain":"flywheel.in"}`), CreatedBy: "seed"})
	regE = must(store.AppendInput{EntryType: "registry", Namespace: "flywheel", Registry: "participants", PayloadRaw: []byte(`{"description":"Beckn participants","schema":{"type":"object"}}`), CreatedBy: "seed"})
	rec1 = must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bap.example.com", PayloadRaw: []byte(`{"role":"BAP","signing_public_key":"k1"}`), CreatedBy: "seed"})
	rec2 = must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bap.example.com", PayloadRaw: []byte(`{"role":"BAP","signing_public_key":"k2"}`), CreatedBy: "seed"})
	return
}

func TestLookupRecordLatest(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, _, rec2 := seedBasic(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants/bap.example.com", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["record_name"] != "bap.example.com" || data["namespace_id"] != "flywheel" {
		t.Fatalf("identity fields: %v", data)
	}
	if data["record_id"] != "flywheel/participants/bap.example.com" {
		t.Fatalf("record_id: %v", data["record_id"])
	}
	if data["version"] != strconv.FormatInt(rec2.Seq, 10) {
		t.Fatalf("version: %v want %d", data["version"], rec2.Seq)
	}
	if data["version_count"].(float64) != 2 {
		t.Fatalf("version_count: %v", data["version_count"])
	}
	details := data["details"].(map[string]any)
	if details["signing_public_key"] != "k2" {
		t.Fatalf("latest details: %v", details)
	}
	if data["state"] != "live" {
		t.Fatalf("state: %v", data["state"])
	}
}

func TestLookupRecordByVersionIDAndAsOn(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, rec1, _ := seedBasic(t, s)
	// version_id pins the older version
	m := getJSON(t, fmt.Sprintf("%s/dedi/lookup/flywheel/participants/bap.example.com?version_id=%d", srv.URL, rec1.Seq), http.StatusOK)
	details := m["data"].(map[string]any)["details"].(map[string]any)
	if details["signing_public_key"] != "k1" {
		t.Fatalf("version_id details: %v", details)
	}
	// as_on at rec1 creation time resolves to rec1
	asOn := rec1.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	m = getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants/bap.example.com?as_on="+asOn, http.StatusOK)
	details = m["data"].(map[string]any)["details"].(map[string]any)
	if details["signing_public_key"] != "k1" {
		t.Fatalf("as_on details: %v", details)
	}
}

func TestLookupNamespaceAndRegistry(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/flywheel", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["name"] != "flywheel" || data["description"] != "Flywheel network" || data["domain"] != "flywheel.in" {
		t.Fatalf("namespace data: %v", data)
	}
	m = getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants", http.StatusOK)
	data = m["data"].(map[string]any)
	if data["registry_name"] != "participants" {
		t.Fatalf("registry data: %v", data)
	}
	if _, hasSchema := data["schema"].(map[string]any); !hasSchema {
		t.Fatalf("registry schema missing: %v", data)
	}
}

func TestLookupErrors(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/nope", http.StatusNotFound)
	if m["code"] != "NOT_FOUND" {
		t.Fatalf("code: %v", m["code"])
	}
	m = getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants/bap.example.com?as_on=garbage", http.StatusBadRequest)
	if m["code"] != "INVALID_REQUEST" {
		t.Fatalf("code: %v", m["code"])
	}
	m = getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants/bap.example.com?version_id=notanumber", http.StatusBadRequest)
	if m["code"] != "INVALID_REQUEST" {
		t.Fatalf("code: %v", m["code"])
	}
}

// A revoked record must stop resolving on the direct three-part path, not just
// on the Beckn wildcard one. ONIX's LookupNode reads neither `state` nor
// `status` and treats any 200 as a live participant, so anything short of a
// non-200 leaves a revoked participant routable and its signatures trusted.
func TestLookupRecordRevokedDoesNotResolve(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, _, rec2 := seedBasic(t, s)
	ctx := context.Background()
	revoked, err := s.Append(ctx, store.AppendInput{
		EntryType: "record", Namespace: "flywheel", Registry: "participants",
		RecordName: "bap.example.com", PayloadRaw: rec2.PayloadRaw,
		State: "revoked", CreatedBy: "seed",
	})
	if err != nil {
		t.Fatalf("revoke append: %v", err)
	}

	base := srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com"
	getJSON(t, base, http.StatusNotFound)

	// History must stay reachable: the revocation withdraws the live binding,
	// it does not hide the record.
	m := getJSON(t, base+"?include_revoked=true", http.StatusOK)
	if got := m["data"].(map[string]any)["state"]; got != "revoked" {
		t.Fatalf("include_revoked state = %v, want revoked", got)
	}
	m = getJSON(t, base+"?version_id="+strconv.FormatInt(rec2.Seq, 10), http.StatusOK)
	if got := m["data"].(map[string]any)["state"]; got != "live" {
		t.Fatalf("pinned read state = %v, want live", got)
	}
	if revoked.VersionNum != 3 {
		t.Fatalf("revoked version_num = %d, want 3", revoked.VersionNum)
	}
}
