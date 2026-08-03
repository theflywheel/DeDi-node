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
