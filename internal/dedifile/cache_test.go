package dedifile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// TestCacheServesTheSameBytesAndNoticesAWrite is the whole contract in one
// test: a hit must be indistinguishable from a rebuild, and a write must not
// be servable from a stale copy. The second half is the one that matters —
// a directory cache that misses a revocation is worse than no cache.
func TestCacheServesTheSameBytesAndNoticesAWrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "a", PayloadRaw: []byte(`{"v":1}`)})

	priv, kid := testSigner(t)
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cfg := Config{Domain: "d.example", BaseURL: "https://d.example",
		Signer: priv, Kid: kid, Now: at, Freshness: time.Hour}

	var c Cache
	first, _, err := c.Get(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := c.Get(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, first, second) {
		t.Fatal("a cache hit returned different bytes from the build it memoized")
	}
	// Against an uncached build too, so the cache is proven to reproduce
	// Build's answer rather than merely to be self-consistent.
	direct, _, err := Build(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, first, direct) {
		t.Fatal("cached manifest differs from an uncached Build of the same inputs")
	}

	// A revocation is the case that must never be served stale.
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "a", PayloadRaw: []byte(`{"v":1}`), State: "revoked"})
	after, _, err := c.Get(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if jsonEqual(t, first, after) {
		t.Fatal("the log head moved and the cache still served the previous manifest")
	}
	if len(after.Files) != 2 {
		t.Fatalf("after revoking, want the keys file plus a revocations file, got %d", len(after.Files))
	}
}

// TestCacheSteppingTheFreshnessWindowRebuilds guards the one input that moves
// without the log moving. next_update is quantized to the window (task #50),
// so a cache keyed only on the log head would keep serving a next_update that
// has already passed — publishing a file that declares itself stale.
func TestCacheSteppingTheFreshnessWindowRebuilds(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})

	priv, kid := testSigner(t)
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cfg := func(at time.Time) Config {
		return Config{Domain: "d.example", BaseURL: "https://d.example",
			Signer: priv, Kid: kid, Now: at, Freshness: time.Hour}
	}

	var c Cache
	early, _, err := c.Get(ctx, s, cfg(base.Add(5*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	late, _, err := c.Get(ctx, s, cfg(base.Add(55*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if early.NextUpdate != late.NextUpdate {
		t.Fatalf("same window gave different next_update: %s vs %s", early.NextUpdate, late.NextUpdate)
	}
	next, _, err := c.Get(ctx, s, cfg(base.Add(65*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if next.NextUpdate == early.NextUpdate {
		t.Fatalf("crossing the window boundary did not advance next_update (%s)", next.NextUpdate)
	}
}

// TestCacheFileMatchesTheManifestItIsListedIn is why File shares Get's build
// rather than doing its own. The manifest commits to a sha-256 of each file
// (spec §6.3); if the two came from separate builds the digest would be
// checked against bytes that were never served.
func TestCacheFileMatchesTheManifestItIsListedIn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "a", PayloadRaw: []byte(`{"v":1}`)})

	priv, kid := testSigner(t)
	cfg := Config{Domain: "d.example", BaseURL: "https://d.example", Signer: priv, Kid: kid,
		Now: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC), Freshness: time.Hour}

	var c Cache
	manifest, _, err := c.Get(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f, ok, err := c.File(ctx, s, cfg, "example.org", "keys")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("File did not find example.org/keys, which the manifest lists")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if want, got := manifest.Files[0].Digest, "sha-256:"+hex.EncodeToString(sum[:]); want != got {
		t.Fatalf("served file digest %s, manifest committed %s", got, want)
	}

	if _, ok, err := c.File(ctx, s, cfg, "example.org", "not-a-registry"); err != nil || ok {
		t.Fatalf("File found a registry this node does not publish (ok=%v err=%v)", ok, err)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ra, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ra) == string(rb)
}
