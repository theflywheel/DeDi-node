package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNodeConfigReplicaRequiresSuppliedOrigin(t *testing.T) {
	body := []byte(`{
		"role":"replica",
		"cluster_id":"main",
		"cluster_peers":"n1=http://127.0.0.1:9001,n2=http://127.0.0.1:9002",
		"shared_key_file":"/data/shared.key"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/node-config", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	(&Server{}).nodeConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "the set's origin") {
		t.Fatalf("error did not mention missing replica origin: %s", rec.Body.String())
	}
}
