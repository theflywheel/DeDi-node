package dedifile

import (
	"context"
	"sync"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Cache memoizes one Build across the requests that would produce identical
// bytes.
//
// Build is O(registries) signatures: every request signs one file per
// (namespace, registry) plus the manifest, so a node with a few hundred
// registries spends most of a manifest request inside ed25519.Sign. Worse,
// both handlers pay it — fetching a single file at /dedi-files/{ns}/... builds
// and signs the whole directory to find one entry.
//
// Caching is only sound because Build is already deterministic. Task #50 made
// next_update quantize to the freshness window and every updated_at derive from
// the log rather than the clock, so two builds with the same inputs are
// byte-identical. Before that fix a cached copy would have been a *different*
// wrong answer rather than the same one; the digest a crawler checks in spec
// §7.3 would still not have matched. Here the cache key is precisely the set of
// inputs that can change the bytes, so a hit is indistinguishable from a
// rebuild.
type Cache struct {
	mu     sync.Mutex
	key    cacheKey
	loaded bool
	built  built
}

// cacheKey is every input that can change Build's output.
//
// head is the log's tree size, which is what the file contents project from.
// It is monotonic — the log is append-only, so any publish, revocation or
// replicated apply moves it — which is what lets a cheap integer read stand in
// for "has anything changed."
//
// window is the freshness window ordinal rather than a timestamp: next_update
// is constant within a window and steps at its boundary, so this is the only
// clock-derived input that can alter the bytes.
//
// The origin and key fields are in the key because one binary can answer on
// more than one host (PublicURL unset, so Build takes its identity from the
// request's Host) and the node can rotate its identity underneath us.
type cacheKey struct {
	head      int64
	window    int64
	domain    string
	baseURL   string
	kid       string
	freshness time.Duration
}

// built is one memoized result, with the per-registry index the file handler
// needs so it can answer without a linear scan of every file in the directory.
type built struct {
	manifest Manifest
	files    []File
	byName   map[[2]string]File
}

// Get returns the current build, reusing the last one when nothing that
// affects its bytes has changed.
//
// A nil *Cache builds every time, so a zero-configured server (and every test
// that calls Build directly) keeps the old behaviour.
func (c *Cache) Get(ctx context.Context, st *store.Store, cfg Config) (Manifest, []File, error) {
	b, err := c.get(ctx, st, cfg)
	if err != nil {
		return Manifest{}, nil, err
	}
	return b.manifest, b.files, nil
}

// get is the shared body of Get and File. It returns the whole build by value
// so a caller never reads one field of a build while another request is
// replacing it.
func (c *Cache) get(ctx context.Context, st *store.Store, cfg Config) (built, error) {
	if c == nil {
		return buildIndexed(ctx, st, cfg)
	}
	head, err := st.TreeSize(ctx)
	if err != nil {
		return built{}, err
	}
	f := cfg.freshness()
	key := cacheKey{
		head:      head,
		window:    cfg.now().UnixNano() / int64(f),
		domain:    cfg.Domain,
		baseURL:   cfg.BaseURL,
		kid:       cfg.Kid,
		freshness: f,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.key == key {
		return c.built, nil
	}
	// Held through the build deliberately. The work is CPU-bound signing, so
	// letting N concurrent requests each build their own copy would multiply
	// exactly the cost this cache exists to remove; serialising means the
	// first one pays and the rest return its result.
	b, err := buildIndexed(ctx, st, cfg)
	if err != nil {
		return built{}, err
	}
	c.key, c.loaded, c.built = key, true, b
	return b, nil
}

// buildIndexed is Build plus the per-registry index the file handler reads.
func buildIndexed(ctx context.Context, st *store.Store, cfg Config) (built, error) {
	manifest, files, err := Build(ctx, st, cfg)
	if err != nil {
		return built{}, err
	}
	byName := make(map[[2]string]File, len(files))
	for _, f := range files {
		byName[[2]string{f.Namespace, f.Registry.Name}] = f
	}
	return built{manifest, files, byName}, nil
}

// File returns one registry's file, or false if this node does not publish it.
// It shares Get's build, so serving a single file costs one map lookup rather
// than a fresh signature per registry in the whole directory.
func (c *Cache) File(ctx context.Context, st *store.Store, cfg Config, ns, registry string) (File, bool, error) {
	b, err := c.get(ctx, st, cfg)
	if err != nil {
		return File{}, false, err
	}
	f, ok := b.byName[[2]string{ns, registry}]
	return f, ok, nil
}
