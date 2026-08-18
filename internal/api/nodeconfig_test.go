package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/provision"
)

// A replica's origin belongs to the SET: every member must sign under the same
// one. Defaulting it fed Spec.Validate a non-empty value and defeated the very
// check meant to catch a missing set origin, rendering exactly the split-origin
// replica this refuses — and because only the leader signs, that config looks
// fine until leadership moves to the odd member.
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
		t.Fatalf("error did not mention the missing replica origin: %s", rec.Body.String())
	}
}

// The same rule at the function that decides it, so the handler test above
// cannot pass for an unrelated reason.
func TestOriginIsNeverDefaultedForAReplica(t *testing.T) {
	if got := originFor(provision.RoleReplica, "", "ha-2"); got != "" {
		t.Errorf("a replica with no supplied origin got %q — Validate would then accept it", got)
	}
	if got := originFor(provision.RoleReplica, "set.example/log", "ha-2"); got != "set.example/log" {
		t.Errorf("a supplied set origin was not used: %q", got)
	}
	// Every other role genuinely has an origin of its own, so a default helps.
	for _, r := range []provision.Role{provision.RoleStandalone, provision.RoleMirror, provision.RoleWitness} {
		if got := originFor(r, "", "n"); got != "n/log" {
			t.Errorf("%s: origin %q, want the default", r, got)
		}
	}
}
