package conformance

// This is the one file in the package allowed to import this repository's
// internal packages (see doc.go). Its only job is producing a running
// instance of the handler under test — internal/api's Handler() — wired to a
// throwaway store and seeded with one namespace, one registry, and one
// record, matching the fixture names fixtureNamespace/fixtureRegistry/
// fixtureRecord used throughout conformance_test.go.
//
// The harness mirrors internal/api/server_test.go's testServer, which is the
// pattern every other handler test in this repo already uses.

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"

	"github.com/theflywheel/DeDi-node/internal/testdb"
)

const (
	fixtureNamespace = "flywheel"
	fixtureRegistry  = "participants"
	fixtureRecord    = "bap.example.com"
)

// startFixtureServer spins up the read-plane handler in-process against a
// real store (TEST_DATABASE_URL, same convention as the rest of the repo),
// seeded with exactly one namespace, one registry, and one record.
func startFixtureServer(t *testing.T) *httptest.Server {
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

	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(store.AppendInput{
		EntryType: "namespace", Namespace: fixtureNamespace,
		PayloadRaw: []byte(`{"description":"Flywheel network","domain":"flywheel.in"}`),
		CreatedBy:  "conformance",
	})
	must(store.AppendInput{
		EntryType: "registry", Namespace: fixtureNamespace, Registry: fixtureRegistry,
		PayloadRaw: []byte(`{"description":"Beckn participants","schema":{"type":"object"}}`),
		CreatedBy:  "conformance",
	})
	must(store.AppendInput{
		EntryType: "record", Namespace: fixtureNamespace, Registry: fixtureRegistry, RecordName: fixtureRecord,
		PayloadRaw: []byte(`{"role":"BAP","signing_public_key":"k1"}`),
		CreatedBy:  "conformance",
	})

	skey, _, err := note.GenerateKey(rand.Reader, "conformance.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "conformance.dedi.local/log", Interval: time.Hour}
	srv := httptest.NewServer((&api.Server{Store: s, CP: cp, TTL: 300}).Handler())
	t.Cleanup(srv.Close)
	return srv
}
