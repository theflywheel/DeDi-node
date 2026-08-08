package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"

	"github.com/theflywheel/DeDi-node/internal/testdb"
)

func testServer(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	url := testdb.URL(t)
	ctx := context.Background()
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	skey, vkey, err := note.GenerateKey(rand.Reader, "test.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "test.dedi.local/log", Interval: time.Hour}
	srv := httptest.NewServer((&Server{Store: s, CP: cp, TTL: 300}).Handler())
	t.Cleanup(srv.Close)
	return srv, s, vkey
}

func getJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s: status %d want %d — body: %s", url, resp.StatusCode, wantStatus, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("GET %s: bad JSON: %v — body: %s", url, err, body)
	}
	return m
}

func TestUnknownRouteIs404JSON(t *testing.T) {
	srv, _, _ := testServer(t)
	m := getJSON(t, srv.URL+"/nope", http.StatusNotFound)
	if m["code"] != "NOT_FOUND" {
		t.Fatalf("error code: %v", m["code"])
	}
}

func TestLogCheckpointServesVerifiableNote(t *testing.T) {
	srv, s, vkey := testServer(t)
	if _, err := s.Append(context.Background(), store.AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/dedi/log/checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	verifier, err := note.NewVerifier(vkey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := note.Open(body, note.VerifierList(verifier)); err != nil {
		t.Fatalf("served checkpoint does not verify: %v", err)
	}
}
