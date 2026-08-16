package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
)

// gatedServer is a node with the write plane open and an operator gate in front
// of it.
func gatedServer(t *testing.T, ns string, gate *AdminAuth) (*httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := publisher.ParseKeySet("op-1:" + ns + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, err := note.GenerateKey(rand.Reader, "gate.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "gate.test/log", Interval: time.Hour},
		TTL:                300,
		WildcardNamespaces: []string{ns},
		Auth:               &publisher.Authenticator{Keys: keys},
		AdminAuth:          gate,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, priv
}

var creds = &AdminAuth{User: "op", Password: "correct horse battery staple"}

// The gate covers the API, not only the page. A signed write from a publisher
// who cannot present operator credentials does not reach the write plane.
func TestTheOperatorGateFrontsTheWriteAPI(t *testing.T) {
	srv, priv := gatedServer(t, "ns", creds)

	resp := signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns", []byte(`{"payload":{}}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without operator credentials", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("WWW-Authenticate = %q, want a Basic challenge so the browser prompts", got)
	}
}

// Operator credentials are a gate, not an authority: they do not let an
// unsigned request write. Both checks have to pass, and only the signature can
// say who wrote.
func TestOperatorCredentialsDoNotReplaceTheSignature(t *testing.T) {
	srv, _ := gatedServer(t, "ns", creds)

	req, err := http.NewRequest("PUT", srv.URL+"/admin/namespaces/ns", strings.NewReader(`{"payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(creds.User, creds.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an unsigned write succeeded on operator credentials alone")
	}
}

// With both, the write lands and is attributed to the publishing key — the
// property a shared password could not provide and the reason it does not
// replace the signature.
func TestWithBothCredentialsAndSignatureTheWriteIsAttributed(t *testing.T) {
	srv, priv := gatedServer(t, "ns", creds)

	resp := signedDo(t, srv, priv, "PUT", "/admin/namespaces/ns", []byte(`{"payload":{}}`),
		publisher.Precondition{IfNoneMatch: "*"})
	defer resp.Body.Close()
	// signedDo does not carry basic auth, so drive it directly instead.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("precondition for this test failed: %d", resp.StatusCode)
	}

	body := []byte(`{"payload":{}}`)
	pre := publisher.Precondition{IfNoneMatch: "*"}
	req, err := http.NewRequest("PUT", srv.URL+"/admin/namespaces/ns", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	req.Header.Set("If-None-Match", pre.IfNoneMatch)
	req.Header.Set(publisher.HeaderKeyID, "op-1")
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, "PUT", "/admin/namespaces/ns", body, pre, now))
	req.SetBasicAuth(creds.User, creds.Password)

	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the write to land", got.StatusCode)
	}
	data := bodyOf(t, got)["data"].(map[string]any)
	if data["created_by"] != "publisher:op-1" {
		t.Errorf("created_by = %v, want the signing key id", data["created_by"])
	}
}

// A wrong password is refused, and so is a wrong username.
func TestWrongCredentialsAreRefused(t *testing.T) {
	srv, _ := gatedServer(t, "ns", creds)
	for _, tc := range []struct{ user, pass string }{
		{"op", "wrong"},
		{"wrong", "correct horse battery staple"},
		{"", ""},
	} {
		req, err := http.NewRequest("GET", srv.URL+"/admin", nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.user != "" || tc.pass != "" {
			req.SetBasicAuth(tc.user, tc.pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%q/%q got %d, want 401", tc.user, tc.pass, resp.StatusCode)
		}
	}
}

// An unconfigured gate leaves the surface as it was. Making credentials
// mandatory would lock out every deployment and signed script on restart, so
// this stays opt-in — the daemon warns instead.
func TestNoGateConfiguredLeavesTheConsoleReachable(t *testing.T) {
	srv, _ := gatedServer(t, "ns", nil)
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the console served when no gate is configured", resp.StatusCode)
	}
}

// The console asks an operator to paste a publisher private key. A script
// injected into this origin, or a clickjacked iframe of that form, is a key
// compromise rather than a defaced page.
func TestTheConsoleIsServedWithAPolicyThatProtectsThePastedKey(t *testing.T) {
	srv, _ := gatedServer(t, "ns", nil)
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	for h, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

// A read-only node has no write plane, so the console has nothing to drive. It
// is then only a public form soliciting a private key.
func TestAReadOnlyNodeDoesNotServeTheConsole(t *testing.T) {
	srv, _, _ := testServer(t) // no Auth, so writeEnabled() is false
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 on a node with no write plane", resp.StatusCode)
	}
}
