package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func TestVersionsRecord(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, rec1, rec2 := seedBasic(t, s)
	m := getJSON(t, srv.URL+"/dedi/versions/flywheel/participants/bap.example.com", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["total_versions"].(float64) != 2 {
		t.Fatalf("total_versions: %v", data["total_versions"])
	}
	vs := data["versions"].([]any)
	if len(vs) != 2 || vs[0] != strconv.FormatInt(rec1.Seq, 10) || vs[1] != strconv.FormatInt(rec2.Seq, 10) {
		t.Fatalf("versions: %v", vs)
	}
	if _, hasSchema := data["schema"].(map[string]any); !hasSchema {
		t.Fatalf("record versions missing registry schema: %v", data)
	}
}

func TestVersionsNamespaceAndRegistry(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)
	m := getJSON(t, srv.URL+"/dedi/versions/flywheel", http.StatusOK)
	if m["data"].(map[string]any)["total_versions"].(float64) != 1 {
		t.Fatalf("namespace versions: %v", m["data"])
	}
	m = getJSON(t, srv.URL+"/dedi/versions/flywheel/participants", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["registry_name"] != "participants" || data["total_versions"].(float64) != 1 {
		t.Fatalf("registry versions: %v", data)
	}
	getJSON(t, srv.URL+"/dedi/versions/nope", http.StatusNotFound)
}

// TestVersionsRegistrySchemaAlwaysPresent guards against the empty-map
// omitempty bug: a registry whose payload has no "schema" key must still
// serialize a "schema": {} field at registry/record versions level, while
// namespace-level versions must omit "schema" entirely.
func TestVersionsRegistrySchemaAlwaysPresent(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, store.AppendInput{
		EntryType:  "namespace",
		Namespace:  "noschemans",
		PayloadRaw: []byte(`{"description":"no schema ns"}`),
		CreatedBy:  "seed",
	}); err != nil {
		t.Fatalf("seed namespace: %v", err)
	}
	if _, err := s.Append(ctx, store.AppendInput{
		EntryType:  "registry",
		Namespace:  "noschemans",
		Registry:   "noschemareg",
		PayloadRaw: []byte(`{"description":"no schema"}`),
		CreatedBy:  "seed",
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	m := getJSON(t, srv.URL+"/dedi/versions/noschemans/noschemareg", http.StatusOK)
	data := m["data"].(map[string]any)
	schema, present := data["schema"]
	if !present {
		t.Fatalf("registry versions missing schema key entirely: %v", data)
	}
	schemaMap, ok := schema.(map[string]any)
	if !ok || len(schemaMap) != 0 {
		t.Fatalf("registry versions schema not empty object: %v (%T)", schema, schema)
	}

	m = getJSON(t, srv.URL+"/dedi/versions/noschemans", http.StatusOK)
	data = m["data"].(map[string]any)
	if _, present := data["schema"]; present {
		t.Fatalf("namespace versions must not include schema key: %v", data)
	}
}
