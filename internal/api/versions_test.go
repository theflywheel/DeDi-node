package api

import (
	"net/http"
	"strconv"
	"testing"
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
