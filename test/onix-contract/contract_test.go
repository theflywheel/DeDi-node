// Package onixcontract proves dedid satisfies the wire contract of the only
// existing open-source DeDi client: beckn-onix's dediregistry plugin. The
// node is exercised strictly over the wire — booted and seeded via its own
// CLI, queried via ONIX's client package.
package onixcontract

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/dediregistry"
)

const (
	bapKeyID  = "76EU7LZ7gfqj13dWDKR1Uitnim11mCoxWBPdzLxUpAMBPVdANKgyFM"
	bapPubKey = "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo="
)

var (
	listenAddr = envOr("DEDID_TEST_ADDR", "127.0.0.1:18080")
	baseURL    = "http://" + listenAddr + "/dedi"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// memCache satisfies definition.Cache (Get/Set/Delete/Clear over strings).
// Note: beckn-onix@v1.8.0's definition.Cache interface adds a Clear(ctx) error
// method beyond the Get/Set/Delete described in the e99b8c1 reference clone —
// this is a mechanical adaptation to the real fetched interface, not a
// behavioral change.
type memCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *memCache) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("cache miss")
}

func (c *memCache) Set(_ context.Context, key, value string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = value
	return nil
}

func (c *memCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
	return nil
}

func (c *memCache) Clear(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[string]string{}
	return nil
}

func newCache() *memCache { return &memCache{m: map[string]string{}} }

// startDedid boots the real binary against the test DB, waits for readiness,
// seeds via the node's own CLI, and tears everything down after the test.
func startDedid(t *testing.T) {
	t.Helper()
	bin := os.Getenv("DEDID_BIN")
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if bin == "" || dbURL == "" {
		t.Skip("DEDID_BIN and TEST_DATABASE_URL required (run via 'make contract-test')")
	}
	keyFile := filepath.Join(t.TempDir(), "dedid.key")
	keygen := exec.Command(bin, "keygen", "-out", keyFile, "-name", "contract.test")
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	serve := exec.Command(bin, "serve")
	serve.Env = append(os.Environ(),
		"DEDI_DB_URL="+dbURL,
		"DEDI_LISTEN="+listenAddr,
		"DEDI_KEY_FILE="+keyFile,
		"DEDI_ORIGIN=contract.test/log",
	)
	serve.Stdout = os.Stderr
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { serve.Process.Kill(); serve.Wait() })

	ready := false
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + listenAddr + "/dedi/log/checkpoint")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("dedid did not become ready on " + listenAddr)
	}

	seed := exec.Command(bin, "seed", "-file", "testdata/beckn-seed.json")
	seed.Env = append(os.Environ(), "DEDI_DB_URL="+dbURL)
	if out, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
}

func newClient(t *testing.T, cfg *dediregistry.Config) *dediregistry.DeDiRegistryClient {
	t.Helper()
	client, closer, err := dediregistry.New(context.Background(), newCache(), cfg)
	if err != nil {
		t.Fatalf("dediregistry.New: %v", err)
	}
	t.Cleanup(func() { closer() })
	return client
}

func TestONIXContract(t *testing.T) {
	startDedid(t)

	t.Run("Lookup_by_subscriber_and_key", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5})
		subs, err := client.Lookup(context.Background(), &model.Subscription{
			Subscriber: model.Subscriber{SubscriberID: "bap.example.com"},
			KeyID:      bapKeyID,
		})
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if len(subs) != 1 {
			t.Fatalf("want 1 subscriber, got %d", len(subs))
		}
		got := subs[0]
		if got.SigningPublicKey != bapPubKey {
			t.Fatalf("signing key: %q", got.SigningPublicKey)
		}
		if got.SubscriberID != "bap.example.com" || got.Type != "BAP" || got.Domain != "retail" {
			t.Fatalf("subscriber fields: %+v", got)
		}
		if len(got.NetworkMemberships) != 1 || got.NetworkMemberships[0] != "beckn.one/testnet" {
			t.Fatalf("network_memberships: %v", got.NetworkMemberships)
		}
	})

	t.Run("Lookup_allowedNetworkIDs_match", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5,
			AllowedNetworkIDs: []string{"beckn.one/testnet"}})
		subs, err := client.Lookup(context.Background(), &model.Subscription{
			Subscriber: model.Subscriber{SubscriberID: "bap.example.com"}, KeyID: bapKeyID})
		if err != nil || len(subs) != 1 {
			t.Fatalf("allowlisted lookup failed: %v (%d)", err, len(subs))
		}
	})

	t.Run("Lookup_allowedNetworkIDs_mismatch_rejected", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5,
			AllowedNetworkIDs: []string{"other-network.org/prod"}})
		_, err := client.Lookup(context.Background(), &model.Subscription{
			Subscriber: model.Subscriber{SubscriberID: "bap.example.com"}, KeyID: bapKeyID})
		if err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Fatalf("want membership rejection, got %v", err)
		}
	})

	t.Run("Lookup_unknown_participant_errors", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5})
		_, err := client.Lookup(context.Background(), &model.Subscription{
			Subscriber: model.Subscriber{SubscriberID: "mallory.example.com"}, KeyID: "no-such-key"})
		if err == nil {
			t.Fatal("unknown participant must error (registry returns non-200)")
		}
	})

	t.Run("LookupNode_by_three_part_id", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5})
		rec, err := client.LookupNode(context.Background(), "beckn-testnet/subscribers.beckn.one/bpp.example.com")
		if err != nil {
			t.Fatalf("LookupNode: %v", err)
		}
		if rec.SubscriberID != "bpp.example.com" {
			t.Fatalf("node record: %+v", rec)
		}
		if rec.Meta["manifestUrl"] != "http://sandbox-bpp:3002/manifest.json" {
			t.Fatalf("node meta: %v", rec.Meta)
		}
	})

	t.Run("LookupRegistry_metadata", func(t *testing.T) {
		client := newClient(t, &dediregistry.Config{URL: baseURL, Timeout: 5})
		md, err := client.LookupRegistry(context.Background(), "beckn-testnet", "subscribers.beckn.one")
		if err != nil {
			t.Fatalf("LookupRegistry: %v", err)
		}
		if md.RawMeta["network"] != "beckn.one/testnet" {
			t.Fatalf("registry meta: %v", md.RawMeta)
		}
	})
}
