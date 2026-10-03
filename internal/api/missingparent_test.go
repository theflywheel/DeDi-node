package api

import (
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
	// Missing registry: the namespace exists.
	seeded, s, priv := writeServer(t, "flywheel")
	seedBasic(t, s)
	// Missing namespace: the key is scoped to flywheel, so it has to be a
	// flywheel that was never created, not some other name (that is a 403).
	empty, _, priv2 := writeServer(t, "flywheel")
	for _, c := range []struct {
		srv                         *httptest.Server
		key                         ed25519.PrivateKey
		method, path, body, missing string
	}{
		{seeded, priv, "POST", "/admin/namespaces/flywheel/registries/nope/records/x/publish", `{"payload":{"a":1}}`, `registry flywheel/nope`},
		{empty, priv2, "PUT", "/admin/namespaces/flywheel/registries/r", `{"payload":{"description":"d","schema":{"type":"object"}}}`, `namespace "flywheel"`},
	} {
		srv, priv := c.srv, c.key
		resp := signedDo(t, srv, priv, c.method, c.path, []byte(c.body), create)
		var body struct{ Error, Code string }
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || body.Code != "NOT_FOUND" {
			t.Errorf("%s %s: %d %s, want 404 NOT_FOUND", c.method, c.path, resp.StatusCode, body.Code)
		}
		if !strings.Contains(body.Error, c.missing) {
			t.Errorf("%s %s: error %q does not name the missing parent %q", c.method, c.path, body.Error, c.missing)
		}
	}
}
