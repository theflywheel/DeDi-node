package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// seedUnderscoreNamespace writes a namespace/registry/record triple under
// `_witness`, mirroring what internal/witness writes, so the read handlers
// have something real to hide or reveal.
func seedUnderscoreNamespace(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "_witness", PayloadRaw: []byte(`{"description":"witness verdicts"}`), CreatedBy: "witness"})
	must(store.AppendInput{EntryType: "registry", Namespace: "_witness", Registry: "target.test", PayloadRaw: []byte(`{"description":"verdicts for target.test"}`), CreatedBy: "witness"})
	must(store.AppendInput{EntryType: "record", Namespace: "_witness", Registry: "target.test", RecordName: "checkpoint", PayloadRaw: []byte(`{"size":1}`), CreatedBy: "witness"})
}

func TestUnderscoreNamespaceHiddenFromSpecEndpoints(t *testing.T) {
	srv, s, _ := testServer(t)
	seedUnderscoreNamespace(t, s)

	paths := []string{
		"/dedi/lookup/_witness",
		"/dedi/lookup/_witness/target.test",
		"/dedi/lookup/_witness/target.test/checkpoint",
		"/dedi/query/_witness",
		"/dedi/query/_witness/target.test",
		"/dedi/versions/_witness",
		"/dedi/versions/_witness/target.test",
		"/dedi/versions/_witness/target.test/checkpoint",
	}
	for _, p := range paths {
		m := getJSON(t, srv.URL+p, http.StatusNotFound)
		if m["code"] != "NOT_FOUND" {
			t.Fatalf("%s: error code = %v, want NOT_FOUND", p, m["code"])
		}
	}
}

func TestUnderscoreNamespaceVisibleWithInternalParam(t *testing.T) {
	srv, s, _ := testServer(t)
	seedUnderscoreNamespace(t, s)

	paths := []string{
		"/dedi/lookup/_witness?internal=1",
		"/dedi/lookup/_witness/target.test?internal=1",
		"/dedi/lookup/_witness/target.test/checkpoint?internal=1",
		"/dedi/query/_witness?internal=1",
		"/dedi/query/_witness/target.test?internal=1",
		"/dedi/versions/_witness?internal=1",
		"/dedi/versions/_witness/target.test?internal=1",
		"/dedi/versions/_witness/target.test/checkpoint?internal=1",
	}
	for _, p := range paths {
		getJSON(t, srv.URL+p, http.StatusOK)
	}
}

// TestNormalNamespaceUnaffected guards against the guard: a namespace that
// merely contains an underscore, or has none at all, must never be hidden.
func TestNormalNamespaceUnaffected(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)

	getJSON(t, srv.URL+"/dedi/lookup/flywheel", http.StatusOK)
	getJSON(t, srv.URL+"/dedi/query/flywheel", http.StatusOK)
	getJSON(t, srv.URL+"/dedi/versions/flywheel", http.StatusOK)
}
