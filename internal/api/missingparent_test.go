package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/publisher"
)

// A write under a parent that does not exist is the caller's mistake, and the
// answer must say which parent (#70). It was a 500 with no detail.
func TestAWriteUnderAMissingParentSaysWhichParent(t *testing.T) {
	create := publisher.Precondition{IfNoneMatch: "*"}
	check := func(srv *httptest.Server, key ed25519.PrivateKey, method, path, body, missing string) {
		t.Helper()
		resp := signedDo(t, srv, key, method, path, []byte(body), create)
		var b struct{ Error, Code string }
		json.NewDecoder(resp.Body).Decode(&b)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || b.Code != "NOT_FOUND" {
			t.Errorf("%s %s: %d %s, want 404 NOT_FOUND", method, path, resp.StatusCode, b.Code)
		}
		if !strings.Contains(b.Error, missing) {
			t.Errorf("%s %s: error %q does not name the missing parent %q", method, path, b.Error, missing)
		}
	}

	// Missing namespace. The key is scoped to flywheel, so it has to be a
	// flywheel that was never created; any other name is a 403. Runs first:
	// every test server truncates the shared database when it starts, so a
	// server created after seeding would wipe the seed.
	empty, _, k1 := writeServer(t, "flywheel")
	check(empty, k1, "PUT", "/admin/namespaces/flywheel/registries/r",
		`{"payload":{"description":"d","schema":{"type":"object"}}}`, `namespace "flywheel"`)

	// Missing registry under a namespace that exists, the case #70 names.
	seeded, s, k2 := writeServer(t, "flywheel")
	seedBasic(t, s)
	if _, err := s.Resolve(context.Background(), "namespace", "flywheel", "", "", nil, nil); err != nil {
		t.Fatalf("the namespace this case depends on is not there: %v", err)
	}
	check(seeded, k2, "POST", "/admin/namespaces/flywheel/registries/nope/records/x/publish",
		`{"payload":{"a":1}}`, `registry flywheel/nope`)
}
