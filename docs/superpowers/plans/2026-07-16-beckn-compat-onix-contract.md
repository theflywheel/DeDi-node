# Beckn Compat + ONIX Contract Proof Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** dedid answers the exact wire contract the Beckn ONIX `dediregistry` plugin speaks, proven by a contract test that drives a real `dedid` process over the wire using ONIX's own client package and dedid's own CLI — no internal test shortcuts.

**Architecture:** Two small additions to dedid (a wildcard-lookup resolver for the `subscribers.beckn.one` registry segment, and a `network_memberships` lift into the record DTO), plus a separate Go module `test/onix-contract` that imports `github.com/beckn-one/beckn-onix/pkg/plugin/implementation/dediregistry` (MIT), boots `bin/dedid serve` as a subprocess, seeds it via `dedid seed`, and asserts `Lookup`/`LookupNode`/`LookupRegistry` behave correctly. Acceptance philosophy (user directive): the node is exercised only through its own client and existing ecosystem implementations.

**Tech Stack:** Existing dedid stack (Go, pgx, sumdb/tlog); contract module additionally depends on `github.com/beckn-one/beckn-onix@v1.8.0` (requires go ≥1.26 — isolated in its own module so the main module keeps its current directive).

## Global Constraints

- Main module (`github.com/theflywheel/DeDi-node`) dependency set stays exactly `github.com/jackc/pgx/v5` + `golang.org/x/mod`. The beckn-onix dependency lives ONLY in `test/onix-contract/go.mod` (separate module, excluded from the root `go test ./...` because nested modules are invisible to the parent).
- Wire contract being satisfied (extracted from beckn-onix@e99b8c1, `pkg/plugin/implementation/dediregistry/`):
  - `GET {base}/lookup/{A}/subscribers.beckn.one/{B}` where for `Lookup` A=subscriber_id, B=key_id; for `LookupNode` A=namespace, B=record_name. `subscribers.beckn.one` is a hardcoded wildcard meaning "search all registries".
  - Success envelope `{message, data}`; `data.details.{signing_public_key, url, type, domain, subscriber_id, encr_public_key}`; `data.network_memberships` as a top-level array under `data`; optional `data.ttl` (seconds, float); `data.created_at`/`updated_at` RFC3339. `signing_public_key` = std-base64 raw Ed25519.
  - Any non-200 status = "unknown participant" (no special body needed).
  - `LookupRegistry` = `GET {base}/lookup/{ns}/{registry}` and requires `data.meta` present (empty object OK).
- Wildcard resolution rule (new, additive — never changes behavior for real registry names): exact resolve first; only on `ErrNotFound` AND registry_name == `subscribers.beckn.one`, search latest live record versions with `record_name = B` and (`namespace = A` OR `payload->>'subscriber_id' = A`), preferring the namespace match, newest first. Only `state='live'` records are eligible (revoked participants must disappear from Beckn lookups).
- Test DB: `TEST_DATABASE_URL` (local Postgres on :5433, `make test` exports the default). All dedid tests remain green (`go test -p 1 ./...`).
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>` (second `-m` flag).

---

### Task 1: Wildcard subscriber search in the store

**Files:**
- Create: `internal/store/beckn.go`
- Test: `internal/store/beckn_test.go`

**Interfaces:**
- Consumes: `Entry`, `entryCols`, `scanEntry`, `ErrNotFound`, `testStore`/`mustAppend`/`seedNSReg` test helpers (all exist in package store).
- Produces: `(*Store).FindBecknSubscriber(ctx context.Context, subject, recordName string) (Entry, error)` — latest live record version named `recordName` where `namespace == subject` OR `payload->>'subscriber_id' == subject`; namespace match preferred, then newest `created_at`; `ErrNotFound` when nothing matches.

- [ ] **Step 1: Write the failing test**

`internal/store/beckn_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
)

func TestFindBecknSubscriberByPayloadSubscriberID(t *testing.T) {
	s := testStore(t)
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-abc",
		PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","signing_public_key":"k1"}`), CreatedBy: "t"})

	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-abc")
	if err != nil {
		t.Fatalf("find by subscriber_id: %v", err)
	}
	if e.Namespace != "beckn-testnet" || e.RecordName != "key-abc" {
		t.Fatalf("wrong entry: %+v", e)
	}
}

func TestFindBecknSubscriberPrefersNamespaceMatch(t *testing.T) {
	s := testStore(t)
	for _, ns := range []string{"ns-a", "bap.example.com"} {
		mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(`{}`), CreatedBy: "t"})
		mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: ns, Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
		mustAppend(t, s, AppendInput{EntryType: "record", Namespace: ns, Registry: "subscribers.beckn.one", RecordName: "key-dup",
			PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","from":"` + ns + `"}`), CreatedBy: "t"})
	}
	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-dup")
	if err != nil {
		t.Fatal(err)
	}
	if e.Namespace != "bap.example.com" {
		t.Fatalf("namespace match not preferred, got ns %q", e.Namespace)
	}
}

func TestFindBecknSubscriberSkipsNonLiveAndMisses(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "key-rev",
		PayloadRaw: []byte(`{"subscriber_id":"gone.example.com"}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "key-rev",
		PayloadRaw: []byte(`{"subscriber_id":"gone.example.com","status":"revoked"}`), State: "revoked", CreatedBy: "t"})

	if _, err := s.FindBecknSubscriber(context.Background(), "gone.example.com", "key-rev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked latest version should be invisible, got %v", err)
	}
	if _, err := s.FindBecknSubscriber(context.Background(), "nobody.example.com", "no-such-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("miss should be ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestFindBeckn -v`
Expected: FAIL to compile — `s.FindBecknSubscriber undefined`.

- [ ] **Step 3: Write the implementation**

`internal/store/beckn.go`:

```go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// FindBecknSubscriber resolves the ONIX dediregistry wildcard lookup
// GET /dedi/lookup/{subject}/subscribers.beckn.one/{recordName}: the latest
// live version of a record named recordName whose namespace is subject or
// whose payload subscriber_id is subject. Namespace matches win, then
// recency. Only live records are visible to Beckn lookups — a revoked
// latest version hides the participant.
func (s *Store) FindBecknSubscriber(ctx context.Context, subject, recordName string) (Entry, error) {
	q := `
WITH latest AS (
  SELECT DISTINCT ON (namespace, registry) ` + entryCols + `
  FROM log_entries
  WHERE entry_type='record' AND record_name=$2
  ORDER BY namespace, registry, version_num DESC
)
SELECT ` + entryCols + ` FROM latest
WHERE state='live' AND (namespace=$1 OR payload->>'subscriber_id'=$1)
ORDER BY (namespace=$1) DESC, created_at DESC
LIMIT 1`
	e, err := scanEntry(s.pool.QueryRow(ctx, q, subject, recordName))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}
```

**Note:** `latest` must carry the `payload` column for the `payload->>'subscriber_id'` filter, and `entryCols` does not include `payload` (only `payload_raw`). Adjust the CTE to select `payload` alongside: replace both `entryCols` usages inside the SQL with an explicit column list — inner select `` + entryCols + `, payload` and outer `SELECT ` + entryCols + ` FROM latest ...`. The outer scan stays `scanEntry`-compatible.

- [ ] **Step 4: Run test to verify it passes**

Run: `TEST_DATABASE_URL='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable' go test ./internal/store/ -run TestFindBeckn -v` then `make test`
Expected: all PASS, no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): beckn wildcard subscriber search for ONIX dediregistry lookups" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: API compat — wildcard route + network_memberships lift

**Files:**
- Modify: `internal/api/lookup.go` (lookupRecord gains wildcard fallback), `internal/api/dto.go` (recordDTO gains NetworkMemberships)
- Test: `internal/api/beckn_compat_test.go`

**Interfaces:**
- Consumes: `store.FindBecknSubscriber` (Task 1), existing `resolveWithVersions`, `recordData`, respond helpers, `testServer`/`getJSON`.
- Produces:
  - `recordDTO` gains `NetworkMemberships []string \`json:"network_memberships,omitempty"\`` populated from the payload's top-level `network_memberships` array (strings only; non-strings skipped). Emitted for ALL record lookups (additive).
  - `lookupRecord`: when the exact resolve is `ErrNotFound` AND `registry_name == "subscribers.beckn.one"` AND no `version_id`/`as_on` params were given, fall back to `FindBecknSubscriber(namespace, record_name)`; on success respond with the found record's real identity (its own ns/registry names in the DTO) and its full version list. Everything else (including proof param handling) flows through the existing `respondLookup`.
  - Constant `becknWildcardRegistry = "subscribers.beckn.one"` in lookup.go.

- [ ] **Step 1: Write the failing test**

`internal/api/beckn_compat_test.go`:

```go
package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func seedBeckn(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{"description":"beckn testnet"}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{"description":"participants"}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-bap-1",
		PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","url":"http://sandbox-bap:3001","type":"BAP","domain":"retail","signing_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","encr_public_key":"g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=","network_memberships":["beckn.one/testnet"]}`), CreatedBy: "seed"})
}

func TestBecknWildcardLookupBySubscriberID(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	// ONIX Lookup shape: /dedi/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}
	m := getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/key-bap-1", http.StatusOK)
	data := m["data"].(map[string]any)
	details := data["details"].(map[string]any)
	if details["signing_public_key"] != "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=" || details["subscriber_id"] != "bap.example.com" {
		t.Fatalf("details: %v", details)
	}
	nm := data["network_memberships"].([]any)
	if len(nm) != 1 || nm[0] != "beckn.one/testnet" {
		t.Fatalf("network_memberships: %v", data["network_memberships"])
	}
	if _, hasTTL := data["ttl"].(float64); !hasTTL {
		t.Fatalf("ttl missing: %v", data["ttl"])
	}
}

func TestBecknWildcardLookupExactStillWins(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	// LookupNode shape: real namespace + literal registry name = plain exact lookup
	m := getJSON(t, srv.URL+"/dedi/lookup/beckn-testnet/subscribers.beckn.one/key-bap-1", http.StatusOK)
	if m["data"].(map[string]any)["record_name"] != "key-bap-1" {
		t.Fatalf("exact lookup broken: %v", m["data"])
	}
}

func TestBecknWildcardLookupUnknown404(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBeckn(t, s)
	m := getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/subscribers.beckn.one/no-such-key", http.StatusNotFound)
	if m["code"] != "NOT_FOUND" {
		t.Fatalf("code: %v", m["code"])
	}
	// wildcard fallback must NOT trigger for ordinary registry names
	getJSON(t, srv.URL+"/dedi/lookup/bap.example.com/participants/key-bap-1", http.StatusNotFound)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable' go test ./internal/api/ -run TestBeckn -v`
Expected: FAIL — wildcard lookup returns 404 (first test), `network_memberships` missing.

- [ ] **Step 3: Write the implementation**

In `internal/api/dto.go`, add to `recordDTO` (after `Meta`):

```go
	NetworkMemberships []string `json:"network_memberships,omitempty"`
```

Add helper below `parseMeta`:

```go
// parseNetworkMemberships pulls the payload's top-level network_memberships
// string array (ONIX dediregistry reads it beside details, not inside).
func parseNetworkMemberships(raw []byte) []string {
	var p struct {
		NetworkMemberships []any `json:"network_memberships"`
	}
	json.Unmarshal(raw, &p)
	out := make([]string, 0, len(p.NetworkMemberships))
	for _, v := range p.NetworkMemberships {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
```

In `recordData(...)`, set the field:

```go
		NetworkMemberships: parseNetworkMemberships(e.PayloadRaw),
```

In `internal/api/lookup.go`, add the constant and rewrite `lookupRecord`:

```go
// becknWildcardRegistry is ONIX dediregistry's hardcoded "search all
// registries" segment (beckn-onix pkg/plugin/implementation/dediregistry).
const becknWildcardRegistry = "subscribers.beckn.one"

func (s *Server) lookupRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	vid, asOn, err := parseLookupParams(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	e, err := s.Store.Resolve(r.Context(), "record", ns, reg, rec, vid, asOn)
	if errors.Is(err, store.ErrNotFound) && reg == becknWildcardRegistry && vid == nil && asOn == nil {
		e, err = s.Store.FindBecknSubscriber(r.Context(), ns, rec)
	}
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "record")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	versions, err := s.Store.Versions(r.Context(), "record", e.Namespace, e.Registry, e.RecordName)
	if err != nil {
		internal(w, err)
		return
	}
	s.respondLookup(w, r, "Record retrieved successfully", recordData(e, versions, s.TTL), e)
}
```

**Note:** this inlines what `resolveWithVersions` did for the record case (params already parsed once; versions fetched against the FOUND entry's identity, which matters for wildcard hits). `lookupNamespace`/`lookupRegistry` keep using `resolveWithVersions` unchanged. Check imports (`errors`, `store`) are present.

- [ ] **Step 4: Run tests to verify green**

Run: `TEST_DATABASE_URL='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable' go test -p 1 ./...`
Expected: all packages PASS (existing lookup/e2e tests confirm no regression; proof param still works on wildcard hits because respondLookup is shared).

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat(api): ONIX dediregistry compat — wildcard subscriber lookup and network_memberships" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: ONIX contract-test module (drives a real dedid over the wire)

**Files:**
- Create: `test/onix-contract/go.mod`, `test/onix-contract/contract_test.go`, `test/onix-contract/testdata/beckn-seed.json`

**Interfaces:**
- Consumes: `bin/dedid` binary (built by Makefile), a reachable `TEST_DATABASE_URL` Postgres, env `DEDID_BIN` (path to binary).
- Produces: `go test` in `test/onix-contract` boots `dedid serve` on `127.0.0.1:18080`, seeds via `dedid seed` (the node's own client — per the acceptance philosophy, no direct DB writes), then proves `Lookup`, `LookupNode`, `LookupRegistry`, the unknown-participant path, and the allowedNetworkIDs rejection path using ONIX's own client package.

- [ ] **Step 1: Create the module**

```bash
mkdir -p test/onix-contract/testdata
cd test/onix-contract
go mod init github.com/theflywheel/DeDi-node/test/onix-contract
go get github.com/beckn-one/beckn-onix@v1.8.0
```

(This module's go directive will land at beckn-onix's floor, go ≥1.26 — the toolchain auto-downloads. The main module is untouched.)

`test/onix-contract/testdata/beckn-seed.json`:

```json
{
  "namespace": "beckn-testnet",
  "payload": {"description": "Beckn testnet mirror (contract test)"},
  "registries": [
    {
      "name": "subscribers.beckn.one",
      "payload": {"description": "Beckn participants", "meta": {"network": "beckn.one/testnet"}},
      "records": [
        {
          "name": "76EU7LZ7gfqj13dWDKR1Uitnim11mCoxWBPdzLxUpAMBPVdANKgyFM",
          "payload": {"subscriber_id": "bap.example.com", "url": "http://sandbox-bap:3001", "type": "BAP", "domain": "retail", "signing_public_key": "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=", "encr_public_key": "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=", "network_memberships": ["beckn.one/testnet"]}
        },
        {
          "name": "76EU7ofwRCF1aobQkShARrf1PAUsNpHqWUJoynPu9w45YFKmzqaPmy",
          "payload": {"subscriber_id": "bpp.example.com", "url": "http://sandbox-bpp:3002", "type": "BPP", "domain": "retail", "signing_public_key": "CqVy97DW45bcZPPrWIYGe2ldl9C93NFeVciiAEYsvR0=", "encr_public_key": "CqVy97DW45bcZPPrWIYGe2ldl9C93NFeVciiAEYsvR0=", "network_memberships": ["beckn.one/testnet"]}
        },
        {
          "name": "bpp.example.com",
          "payload": {"subscriber_id": "bpp.example.com", "url": "http://sandbox-bpp:3002", "type": "BPP", "domain": "retail", "signing_public_key": "CqVy97DW45bcZPPrWIYGe2ldl9C93NFeVciiAEYsvR0=", "encr_public_key": "CqVy97DW45bcZPPrWIYGe2ldl9C93NFeVciiAEYsvR0=", "meta": {"manifestUrl": "http://sandbox-bpp:3002/manifest.json"}}
        }
      ]
    }
  ]
}
```

(Key IDs and public keys are the starter kit's committed testnet identities — `generic-devkit/config/generic-bap.yaml` / `generic-bpp.yaml` — so this same seed powers the Plan-B starter-kit run. The third record is the `LookupNode`-shaped node record keyed by subscriber_id.)

- [ ] **Step 2: Write the contract test**

`test/onix-contract/contract_test.go`:

```go
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
	listenAddr = "127.0.0.1:18080"
	baseURL    = "http://127.0.0.1:18080/dedi"
	bapKeyID   = "76EU7LZ7gfqj13dWDKR1Uitnim11mCoxWBPdzLxUpAMBPVdANKgyFM"
	bapPubKey  = "g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo="
)

// memCache satisfies definition.Cache (Get/Set/Delete over strings).
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
```

**Note for the implementer:** exact struct/method shapes above were verified against beckn-onix@e99b8c1 (`Config`, `New(ctx, cache, cfg) (*DeDiRegistryClient, func() error, error)`, `definition.Cache` = Get/Set/Delete). If `v1.8.0` differs in a signature, adapt the calls minimally and record the difference in your report — do NOT change dedid to fit; the contract lives on the dedid side of the wire, not in Go signatures. A cloned copy of beckn-onix is available at `/private/tmp/claude-501/-Users---chaks--/66bc09d8-aaa4-4aa5-99e8-68f3f2c9ab04/scratchpad/beckn-onix` for reference. If the rejection-message substring in the allowlist test differs, match the actual message.

- [ ] **Step 3: Run to verify it fails, then passes**

First run (before Tasks 1–2 are merged this fails; in execution order they're already in):

```bash
cd /Users/__chaks__/code/DeDi-node && make build
cd test/onix-contract && DEDID_BIN=/Users/__chaks__/code/DeDi-node/bin/dedid TEST_DATABASE_URL='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable' go test -v ./...
```

Expected: all 6 subtests PASS. Note: reruns accumulate record versions in the shared test DB — lookups return latest, so the test is rerun-safe; if the DB has conflicting leftovers from other suites, `make test` (which truncates via its own helpers) can be run first.

- [ ] **Step 4: Commit**

```bash
git add test/onix-contract
git commit -m "test(contract): prove dedid against beckn-onix dediregistry client v1.8.0" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Wire-up — Makefile target, docs, design addendum

**Files:**
- Modify: `Makefile`, `README.md`, `docs/design.md`

**Interfaces:**
- Consumes: everything above.
- Produces: `make contract-test`; README "Compatibility" section; design.md Addendum C recording the extracted ONIX wire contract and the locked-URL finding.

- [ ] **Step 1: Makefile target**

Append to `Makefile`:

```makefile
contract-test: build
	cd test/onix-contract && DEDID_BIN=$(CURDIR)/bin/dedid go test -v ./...
```

(`TEST_DATABASE_URL` is already exported at the top of the Makefile.)

- [ ] **Step 2: README section**

Add after the Development section:

```markdown
## Compatibility

dedid is proven from the outside in — through its own CLI and existing ecosystem clients, never bespoke harnesses:

- `make test` — the node's own integration suite (includes offline inclusion-proof verification).
- `make contract-test` — boots a real `dedid`, seeds it via `dedid seed`, and runs the [beckn-onix `dediregistry` client](https://github.com/beckn/beckn-onix) (the ONIX adapter's registry plugin, pinned to v1.8.0) against it over the wire: subscriber key lookup, node lookup, registry metadata, network-membership enforcement, and unknown-participant rejection.

Note: a stock ONIX adapter pins the registry URL to `fabric.nfh.global` via a signed "locked Beckn constant" — pointing a full ONIX deployment at a self-hosted registry currently requires a patched adapter build. See docs/design.md Addendum C.
```

- [ ] **Step 3: design.md Addendum C**

Append to `docs/design.md`:

```markdown
## Addendum C — ONIX `dediregistry` wire contract (M3 teardown, 2026-07-16)

Extracted from beckn-onix@e99b8c1 (v1.8.0). Supersedes §6.1's assumptions; resolves risk R1.

- Lookup (signature-validation hot path): `GET {url}/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}` — the middle segment is a hardcoded wildcard meaning "search all registries". dedid implements this via exact-resolve-first, then wildcard fallback (`FindBecknSubscriber`), live records only.
- Node lookup: `GET {url}/lookup/{ns}/{registry}/{record}`; registry metadata: `GET {url}/lookup/{ns}/{registry}` (requires `data.meta`).
- Response contract: `{message, data}` with `data.details.{signing_public_key (std-base64 raw Ed25519), url, type, domain, subscriber_id, encr_public_key}`, `data.network_memberships []string` (enforced client-side against `allowedNetworkIDs` — absent list + configured allowlist ⇒ rejection), optional `data.ttl` seconds (client cache override). Any non-200 ⇒ unknown participant ⇒ ONIX responds 401 NACK.
- NOT used by ONIX: subscribe/on_subscribe (onboarding is out-of-band), registry-based routing (callback URLs come from `context.bpp_uri`), response signing. VC revocation is a bare GET on a credential-embedded lookup URL: 200 ⇒ revoked, 404/410 ⇒ not revoked.
- **Locked constant:** stock ONIX force-injects `dediregistry.url = https://fabric.nfh.global/registry/dedi` from a signature-verified embedded constants file (plugin-manager enforcement on the exact plugin id `dediregistry`; any other configured value fails startup). Consequences: (a) the §6.3 acceptance test requires a patched/forked adapter build or a network-level override; (b) an upstream change request is needed before "change only the registry URL" is honest for stock deployments; (c) importing the plugin's Go package directly bypasses the lock — which is how the contract test works.
```

- [ ] **Step 4: Run everything once more, commit, push**

```bash
make test && make contract-test
git add Makefile README.md docs/design.md
git commit -m "docs: contract-test target, compatibility section, ONIX wire-contract addendum" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
git push
```

---

## Out of scope (Plan B — starter-kit E2E harness, separate plan)

- Forked/patched beckn-onix build making the locked registry URL overridable (env-gated), adapter image build, compose overlay with dedid + Postgres, Postman/newman-driven discover→select→init→confirm run, negative test (record revoked in dedid ⇒ 401 NACK), upstream PR drafts (beckn-onix unlock proposal + starter-kit doc fixes). Requires a Docker-capable host.
