package api

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// seedSubscribableRegistry gives the tests a registry to subscribe to. A
// subscription to a registry that does not exist is refused, so this is not
// optional setup.
func seedSubscribableRegistry(t *testing.T, s *Server, ns, reg string) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Store.Append(ctx, store.AppendInput{
		EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Append(ctx, store.AppendInput{
		EntryType: "registry", Namespace: ns, Registry: reg, PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
}

func subscribeVia(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, kid, ns, reg, target string) *http.Response {
	t.Helper()
	return signedAs(t, srv, priv, kid, http.MethodPost,
		"/admin/namespaces/"+ns+"/registries/"+reg+"/subscriptions",
		mustJSON(subscriptionRequest{TargetURL: target}))
}

func TestASubscriptionIsRegisteredAndListed(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	api.AllowPrivateWebhookTargets = true
	seedSubscribableRegistry(t, api, "beckn", "subscribers")

	resp := subscribeVia(t, srv, priv, "op-1", "beckn", "subscribers", "https://consumer.example/hook")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, raw)
	}
	var created struct {
		Data subscriptionDTO `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.ID == "" || created.Data.State != "active" {
		t.Fatalf("unexpected subscription: %+v", created.Data)
	}

	listed := signedAs(t, srv, priv, "op-1", http.MethodGet, "/admin/namespaces/beckn/subscriptions", nil)
	defer listed.Body.Close()
	body, _ := io.ReadAll(listed.Body)
	if !strings.Contains(string(body), created.Data.ID) {
		t.Fatalf("the subscription is not in the list: %s", body)
	}

	del := signedAs(t, srv, priv, "op-1", http.MethodDelete,
		"/admin/namespaces/beckn/subscriptions/"+created.Data.ID, nil)
	defer del.Body.Close()
	if del.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(del.Body)
		t.Fatalf("delete: %d %s", del.StatusCode, b)
	}
	active, err := api.Store.ActiveSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("a deleted subscription is still live: %+v", active)
	}
}

// Registering a subscription is a standing instruction the node will act on,
// so it is a publisher write like any other: signed, and scoped to the
// namespace in the path.
func TestSubscriptionWritesRequireASignatureAndTheRightScope(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn", "other")
	api.AllowPrivateWebhookTargets = true
	seedSubscribableRegistry(t, api, "beckn", "subscribers")

	unsigned, err := http.Post(srv.URL+"/admin/namespaces/beckn/registries/subscribers/subscriptions",
		"application/json", strings.NewReader(`{"target_url":"https://consumer.example/hook"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer unsigned.Body.Close()
	if unsigned.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned subscription got %d, want 401", unsigned.StatusCode)
	}

	// op-2 is scoped to "other" and must not be able to subscribe on beckn's
	// behalf: a key valid for one namespace answering for another is the
	// escalation the per-namespace grants exist to prevent.
	wrongScope := subscribeVia(t, srv, priv, "op-2", "beckn", "subscribers", "https://consumer.example/hook")
	defer wrongScope.Body.Close()
	if wrongScope.StatusCode != http.StatusForbidden {
		t.Fatalf("a key scoped elsewhere got %d, want 403", wrongScope.StatusCode)
	}
}

// A subscription carries only an id in its command, and the path constrains the
// namespace but not which subscription is named. Without an ownership check,
// a key scoped to one namespace could delete another namespace's subscription
// and silently stop its revocations being pushed.
func TestASubscriptionCannotBeDeletedFromAnotherNamespace(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn", "other")
	api.AllowPrivateWebhookTargets = true
	seedSubscribableRegistry(t, api, "beckn", "subscribers")
	seedSubscribableRegistry(t, api, "other", "things")

	resp := subscribeVia(t, srv, priv, "op-1", "beckn", "subscribers", "https://consumer.example/hook")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created struct {
		Data subscriptionDTO `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}

	del := signedAs(t, srv, priv, "op-2", http.MethodDelete,
		"/admin/namespaces/other/subscriptions/"+created.Data.ID, nil)
	defer del.Body.Close()
	if del.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting beckn's subscription through other got %d, want 404", del.StatusCode)
	}
	active, err := api.Store.ActiveSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("beckn's subscription was deleted from another namespace: %+v", active)
	}
}

// The node fetches these URLs itself, from inside the operator's network. An
// unchecked target is a request forgery primitive — and 169.254.169.254 is the
// one that matters: on every major cloud platform it hands instance
// credentials to whatever asks.
func TestPrivateAndMetadataTargetsAreRefusedByDefault(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	seedSubscribableRegistry(t, api, "beckn", "subscribers")

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://127.0.0.1:8080/hook",
		"http://localhost/hook",
		"http://[::1]/hook",
		"file:///etc/passwd",
		"gopher://consumer.example/hook",
		"",
	} {
		resp := subscribeVia(t, srv, priv, "op-1", "beckn", "subscribers", target)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("target %q was accepted with %d: %s", target, resp.StatusCode, body)
		}
	}

	stored, err := api.Store.AllSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("a refused target was stored anyway: %+v", stored)
	}
}

// publicIP is what the whole SSRF gate turns on, so it is checked directly as
// well as through the handler — the handler test cannot reach every range
// without doing DNS lookups the test environment may not allow.
func TestPublicIPRejectsEveryUnroutableRange(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1", "::1", // loopback
		"0.0.0.0", "::", // unspecified
		"169.254.169.254", "fe80::1", // link-local — the cloud metadata service
		"10.0.0.5", "172.16.0.1", "192.168.1.1", "fd00::1", // private
		"224.0.0.1", "ff02::1", // multicast
	} {
		if publicIP(mustIP(t, addr)) {
			t.Errorf("%s is treated as a public target", addr)
		}
	}
	for _, addr := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if !publicIP(mustIP(t, addr)) {
			t.Errorf("%s is a public address but was refused", addr)
		}
	}
}

// A subscription to a registry that does not exist is always a typo, and it
// would sit in the console looking exactly like one that works.
func TestSubscribingToAMissingRegistryIsRefused(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	api.AllowPrivateWebhookTargets = true
	seedSubscribableRegistry(t, api, "beckn", "subscribers")

	resp := subscribeVia(t, srv, priv, "op-1", "beckn", "no-such-registry", "https://consumer.example/hook")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("subscribing to a missing registry got %d, want 404", resp.StatusCode)
	}
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test address %q", s)
	}
	return ip
}
