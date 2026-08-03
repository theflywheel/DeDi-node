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
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// writeServer boots a node with the write plane open for one namespace.
func writeServer(t *testing.T, ns string) (*httptest.Server, *store.Store, ed25519.PrivateKey) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close() // only needed for its store setup

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := publisher.ParseKeySet("op-1:" + ns + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	// A real checkpointer, so published records are provable here exactly as
	// they are on a running node.
	skey, _, err := note.GenerateKey(rand.Reader, "write.test")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "write.test/log", Interval: time.Hour}
	srv := httptest.NewServer((&Server{
		Store: s, CP: cp, TTL: 300,
		WildcardNamespaces: []string{ns},
		Auth:               &publisher.Authenticator{Keys: keys},
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, s, priv
}

// signedDo issues a request signed by priv, or unsigned when priv is nil.
func signedDo(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, method, path string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if priv != nil {
		now := time.Now().UTC()
		req.Header.Set(publisher.HeaderKeyID, "op-1")
		req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
		req.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, method, path, body, now))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bad JSON (status %d): %s", resp.StatusCode, raw)
	}
	return m
}

// The whole point: onboard a participant over the wire, then resolve it the way
// ONIX would.
func TestPublishThenResolveAsONIX(t *testing.T) {
	srv, _, priv := writeServer(t, "beckn-testnet")

	resp := signedDo(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet", []byte(`{"payload":{"description":"testnet"}}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put namespace: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	resp = signedDo(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one", []byte(`{"payload":{"description":"participants"}}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put registry: %d %v", resp.StatusCode, bodyOf(t, resp))
	}

	rec := `{"payload":{"subscriber_id":"bpp.acme.example","type":"BPP","url":"https://bpp.acme.example/beckn",` +
		`"signing_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","network_memberships":["beckn.one/testnet"]}}`
	resp = signedDo(t, srv, priv, "POST", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one/records/KEY-1/publish", []byte(rec))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	data := bodyOf(t, resp)["data"].(map[string]any)
	if data["created_by"] != "publisher:op-1" {
		t.Fatalf("created_by = %v, want the publishing key id", data["created_by"])
	}
	if data["state"] != "live" {
		t.Fatalf("state = %v", data["state"])
	}

	// The ONIX wildcard path must now resolve it.
	m := getJSON(t, srv.URL+"/dedi/lookup/bpp.acme.example/subscribers.beckn.one/KEY-1", http.StatusOK)
	details := m["data"].(map[string]any)["details"].(map[string]any)
	if details["signing_public_key"] != "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=" || details["type"] != "BPP" {
		t.Fatalf("details: %v", details)
	}
}

func TestRevokeHidesFromBecknLookup(t *testing.T) {
	srv, _, priv := writeServer(t, "beckn-testnet")
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet", []byte(`{"payload":{}}`)).Body.Close()
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one", []byte(`{"payload":{}}`)).Body.Close()
	pub := `{"payload":{"subscriber_id":"bpp.acme.example","type":"BPP"}}`
	signedDo(t, srv, priv, "POST", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one/records/KEY-1/publish", []byte(pub)).Body.Close()
	getJSON(t, srv.URL+"/dedi/lookup/bpp.acme.example/subscribers.beckn.one/KEY-1", http.StatusOK)

	resp := signedDo(t, srv, priv, "POST", "/admin/namespaces/beckn-testnet/registries/subscribers.beckn.one/records/KEY-1/revoke", []byte(`{"reason":"key compromise"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d %v", resp.StatusCode, bodyOf(t, resp))
	}
	if got := bodyOf(t, resp)["data"].(map[string]any)["state"]; got != "revoked" {
		t.Fatalf("state = %v", got)
	}

	// Revoked participants disappear from Beckn resolution — that is what makes
	// ONIX reject their messages.
	getJSON(t, srv.URL+"/dedi/lookup/bpp.acme.example/subscribers.beckn.one/KEY-1", http.StatusNotFound)

	// ...but the history is still there: revocation is an append, not a delete.
	vs := getJSON(t, srv.URL+"/dedi/versions/beckn-testnet/subscribers.beckn.one/KEY-1", http.StatusOK)
	if n := vs["data"].(map[string]any)["total_versions"].(float64); n != 2 {
		t.Fatalf("total_versions = %v, want 2 (publish + revoke)", n)
	}
	// The revoked version carries the reason.
	cur := getJSON(t, srv.URL+"/dedi/lookup/beckn-testnet/subscribers.beckn.one/KEY-1", http.StatusOK)
	if reason := cur["data"].(map[string]any)["details"].(map[string]any)["revocation_reason"]; reason != "key compromise" {
		t.Fatalf("revocation_reason = %v", reason)
	}
}

func TestWritePlaneRejectsUnauthorized(t *testing.T) {
	srv, _, priv := writeServer(t, "beckn-testnet")
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/admin/namespaces/beckn-testnet/registries/r/records/KEY-1/publish"
	body := []byte(`{"payload":{"a":1}}`)

	// Unsigned.
	if resp := signedDo(t, srv, nil, "POST", path, body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned: %d", resp.StatusCode)
	}
	// Signed by a key the node does not hold.
	if resp := signedDo(t, srv, otherPriv, "POST", path, body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign key: %d", resp.StatusCode)
	}
	// Correctly signed, but for a namespace this key has no authority over —
	// 403, because retrying with the same credential will never work.
	const foreign = "/admin/namespaces/other-net/registries/r/records/KEY-1/publish"
	if resp := signedDo(t, srv, priv, "POST", foreign, body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-namespace write: %d, want 403", resp.StatusCode)
	}
	// Body tampered after signing.
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader([]byte(`{"payload":{"a":2}}`)))
	now := time.Now().UTC()
	req.Header.Set(publisher.HeaderKeyID, "op-1")
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, "POST", path, body, now))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered body: %d", resp.StatusCode)
	}
}

// A node with no publisher keys must expose no write surface at all.
func TestWritePlaneAbsentWhenNoKeys(t *testing.T) {
	srv, _, _ := testServer(t)
	req, _ := http.NewRequest("PUT", srv.URL+"/admin/namespaces/beckn-testnet", bytes.NewReader([]byte(`{"payload":{}}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("read-only node answered a write route with %d", resp.StatusCode)
	}
}

func TestPublishRejectsBadPayloads(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	const path = "/admin/namespaces/ns/registries/r/records/rec/publish"
	for _, body := range []string{`{"payload":"a string"}`, `{"payload":[1,2]}`, `{}`, `not json`} {
		resp := signedDo(t, srv, priv, "POST", path, []byte(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q: status %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestRevokeUnknownRecordIs404(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	resp := signedDo(t, srv, priv, "POST", "/admin/namespaces/ns/registries/r/records/nope/revoke", []byte(`{}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

// Writes must land in the transparency log like any other entry, with an
// inclusion proof against the signed checkpoint.
func TestPublishedRecordIsProvable(t *testing.T) {
	srv, s, priv := writeServer(t, "ns")
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns", []byte(`{"payload":{}}`)).Body.Close()
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns/registries/r", []byte(`{"payload":{}}`)).Body.Close()
	signedDo(t, srv, priv, "POST", "/admin/namespaces/ns/registries/r/records/rec/publish", []byte(`{"payload":{"a":1}}`)).Body.Close()

	size, err := s.TreeSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if size < 3 {
		t.Fatalf("tree size = %d, want the three published entries", size)
	}
	m := getJSON(t, srv.URL+"/dedi/lookup/ns/r/rec?proof=inclusion", http.StatusOK)
	if _, ok := m["proof"]; !ok {
		t.Fatalf("no inclusion proof on a published record: %v", m)
	}
}

// A registry schema must reject an incomplete participant at the write, as a
// caller error — not surface later as an ONIX signature failure.
func TestPublishEnforcesRegistrySchema(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns", []byte(`{"payload":{}}`)).Body.Close()
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns/registries/participants",
		[]byte(`{"payload":{"schema":{"type":"object","required":["subscriber_id","signing_public_key"]}}}`)).Body.Close()

	const path = "/admin/namespaces/ns/registries/participants/records/KEY-1/publish"
	resp := signedDo(t, srv, priv, "POST", path, []byte(`{"payload":{"subscriber_id":"a"}}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("incomplete record: status %d, want 400", resp.StatusCode)
	}
	if msg, _ := bodyOf(t, resp)["error"].(string); !strings.Contains(msg, "signing_public_key") {
		t.Fatalf("error should name the missing field, got %q", msg)
	}
	resp = signedDo(t, srv, priv, "POST", path, []byte(`{"payload":{"subscriber_id":"a","signing_public_key":"k"}}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete record: status %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

// setupRegistry publishes a namespace and registry, returning the record path.
func setupRegistry(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, ns, reg, rec string) string {
	t.Helper()
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/"+ns, []byte(`{"payload":{}}`)).Body.Close()
	signedDo(t, srv, priv, "PUT", "/admin/namespaces/"+ns+"/registries/"+reg, []byte(`{"payload":{}}`)).Body.Close()
	return "/admin/namespaces/" + ns + "/registries/" + reg + "/records/" + rec
}

func versionsOf(t *testing.T, srv *httptest.Server, ns, reg, rec string) float64 {
	t.Helper()
	m := getJSON(t, srv.URL+"/dedi/versions/"+ns+"/"+reg+"/"+rec, http.StatusOK)
	return m["data"].(map[string]any)["total_versions"].(float64)
}

// A double-clicked Save — or a captured request replayed inside the signature
// window — must not fork a participant's history.
func TestPublishIsIdempotentForIdenticalPayload(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	body := []byte(`{"payload":{"subscriber_id":"a","type":"BPP"}}`)

	first := bodyOf(t, signedDo(t, srv, priv, "POST", path+"/publish", body))
	if first["data"].(map[string]any)["unchanged"] != false {
		t.Fatalf("first publish reported unchanged: %v", first)
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 1 {
		t.Fatalf("after first publish: %v versions", n)
	}

	// Same payload again: no new version, and the response says so.
	second := bodyOf(t, signedDo(t, srv, priv, "POST", path+"/publish", body))
	data := second["data"].(map[string]any)
	if data["unchanged"] != true {
		t.Fatalf("replay appended a version: %v", second)
	}
	if data["version"] != first["data"].(map[string]any)["version"] {
		t.Fatalf("replay returned a different version: %v vs %v", data["version"], first["data"])
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 1 {
		t.Fatalf("replay forked history: %v versions", n)
	}

	// A genuine change still appends.
	changed := []byte(`{"payload":{"subscriber_id":"a","type":"BAP"}}`)
	if bodyOf(t, signedDo(t, srv, priv, "POST", path+"/publish", changed))["data"].(map[string]any)["unchanged"] != false {
		t.Fatal("a changed payload was treated as a replay")
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 2 {
		t.Fatalf("after real change: %v versions, want 2", n)
	}
}

// If-Match is lost-update protection: two operators editing the same
// participant must not silently overwrite each other.
func TestPublishHonoursIfMatch(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	first := bodyOf(t, signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"v":1}}`)))
	digest := first["data"].(map[string]any)["digest"].(string)

	// Stale digest: refused.
	req := func(ifMatch string, body string) *http.Response {
		r, _ := http.NewRequest("POST", srv.URL+path+"/publish", bytes.NewReader([]byte(body)))
		now := time.Now().UTC()
		r.Header.Set(publisher.HeaderKeyID, "op-1")
		r.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
		r.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, "POST", path+"/publish", []byte(body), now))
		r.Header.Set("If-Match", ifMatch)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := req("0000000000000000000000000000000000000000000000000000000000000000", `{"payload":{"v":2}}`); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status %d, want 412", resp.StatusCode)
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 1 {
		t.Fatalf("refused write still appended: %v versions", n)
	}
	// Current digest: accepted.
	if resp := req(digest, `{"payload":{"v":2}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("current If-Match: status %d, want 200", resp.StatusCode)
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 2 {
		t.Fatalf("accepted write did not append: %v versions", n)
	}
	// If-Match on a record that does not exist yet cannot be satisfied.
	other := "/admin/namespaces/ns/registries/r/records/KEY-NEW/publish"
	r, _ := http.NewRequest("POST", srv.URL+other, bytes.NewReader([]byte(`{"payload":{}}`)))
	now := time.Now().UTC()
	r.Header.Set(publisher.HeaderKeyID, "op-1")
	r.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	r.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, "POST", other, []byte(`{"payload":{}}`), now))
	r.Header.Set("If-Match", digest)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-Match on a new record: status %d, want 412", resp.StatusCode)
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"subscriber_id":"a"}}`)).Body.Close()

	if bodyOf(t, signedDo(t, srv, priv, "POST", path+"/revoke", []byte(`{"reason":"x"}`)))["data"].(map[string]any)["unchanged"] != false {
		t.Fatal("first revoke reported unchanged")
	}
	second := bodyOf(t, signedDo(t, srv, priv, "POST", path+"/revoke", []byte(`{"reason":"x"}`)))
	if second["data"].(map[string]any)["unchanged"] != true {
		t.Fatalf("second revoke appended a version: %v", second)
	}
	if n := versionsOf(t, srv, "ns", "r", "KEY-1"); n != 2 {
		t.Fatalf("versions = %v, want 2 (publish + one revoke)", n)
	}
}

// The decision on expiry: mark, do not filter. An expired participant still
// resolves — including on the Beckn wildcard path — and carries the marker.
func TestExpiredRecordStillResolvesWithMarker(t *testing.T) {
	srv, _, priv := writeServer(t, "beckn-testnet")
	path := setupRegistry(t, srv, priv, "beckn-testnet", "subscribers.beckn.one", "KEY-OLD")
	body := []byte(`{"payload":{"subscriber_id":"bpp.old.example","type":"BPP",` +
		`"valid_from":"2020-01-01T00:00:00Z","valid_until":"2021-01-01T00:00:00Z"}}`)
	signedDo(t, srv, priv, "POST", path+"/publish", body).Body.Close()

	m := getJSON(t, srv.URL+"/dedi/lookup/bpp.old.example/subscribers.beckn.one/KEY-OLD", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["expired"] != true {
		t.Fatalf("expired = %v, want true", data["expired"])
	}
	if data["valid_till"] != "2021-01-01T00:00:00Z" {
		t.Fatalf("valid_till = %v", data["valid_till"])
	}
	if data["not_yet_valid"] != false {
		t.Fatalf("not_yet_valid = %v, want false", data["not_yet_valid"])
	}
	// It is still live and still served — marking, not filtering.
	if data["state"] != "live" {
		t.Fatalf("state = %v", data["state"])
	}
}

// Records with no declared window must not grow the new fields at all.
func TestRecordWithoutWindowHasNoMarkers(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"subscriber_id":"a"}}`)).Body.Close()

	data := getJSON(t, srv.URL+"/dedi/lookup/ns/r/KEY-1", http.StatusOK)["data"].(map[string]any)
	if _, present := data["expired"]; present {
		t.Fatalf("expired present without a declared window: %v", data["expired"])
	}
	if _, present := data["not_yet_valid"]; present {
		t.Fatalf("not_yet_valid present without a declared window")
	}
}
