# DeDi Node — Design Document

**Status:** Draft v0.2 (v0.1 + Addendum A review findings + Addendum B spec teardown)
**Author:** Chakshu (FWAI Technologies / Flywheel)
**Date:** July 2026
**License:** Apache-2.0

---

## 1. Summary

An independent, self-hostable, open-source implementation of the Decentralized Directory (DeDi) protocol — a registry node that lets any organization publish tamper-evident, provenance-enabled public directories behind the standard DeDi API, without depending on the hosted dedi.global platform or its CORD blockchain substrate.

The core architectural bet: DeDi's required trust properties (tamper-resistance, provenance, version history, offline verifiability) are fully satisfied by a **transparency-log architecture** — a Merkle tree over an append-only record log with signed checkpoints and external witnessing — at a fraction of the operational weight of a blockchain-anchored deployment.

A first-class compatibility target is **Beckn**: ONIX v0.6.0 onboards networks against DeDi as the Beckn One registry, making a self-hostable DeDi node the missing piece for anyone running a sovereign Beckn network. "The first self-hostable Beckn One-compatible registry" is the opening claim; "an independent DeDi implementation" is the general one.

## 2. Context and gap

- The DeDi protocol lives as a lab under Linux Foundation Decentralized Trust (`LF-Decentralized-Trust-labs/decentralized-directory-protocol`), Apache-2.0. The repo contains OpenAPI specs (`api/`), reference schemas (`schemas/`), and docs — and explicitly states it is a standard, not a software product.
- The only running implementation is **dedi.global**, a hosted platform operated by Networks for Humanity Foundation, anchored on the CORD blockchain. There is no self-hostable open-source registry node.
- The lab has **no conformance test suite and no releases**. Correctness of "a DeDi implementation" is currently undefined in practice.
- Beckn has made DeDi load-bearing: the ONIX adapter ships a `dediregistry` plugin used for participant identity lookup (signature validation + routing), and the Beckn starter kit treats the DeDi registry as the source of truth for network identity, hosted at `fabric.nfh.global`. ONIX's VC validator also checks revocation against DeDi.

**Opportunity:** an independent implementation + upstream conformance suite + Beckn drop-in compatibility.

## 3. Goals

1. **Spec conformance.** Implement the DeDi lookup/query/versions API per the LFDT OpenAPI specs.
2. **Self-hostability.** Single Go binary + Postgres. `docker compose up` to a working node. No blockchain dependency.
3. **Verifiability.** Every record lookup can return an inclusion proof against a signed checkpoint; relying parties verify offline. Consistency proofs between checkpoints make history rewriting detectable.
4. **Beckn drop-in.** The ONIX `dediregistry` plugin works against this node by changing only the registry URL. Starter-kit network passes end-to-end.
5. **Conformance suite as a separate, upstreamable package.** Contributed to the LFDT lab independent of this implementation.

### Non-goals (v1)

- Multi-tenant SaaS operation (single-operator, multi-namespace is enough).
- CORD/blockchain anchoring (witness cosigning covers the threat model; a CORD anchor can be added later as just another witness).
- Federation/gossip between nodes beyond witnessing.
- Being a general-purpose database. Directories are small, public, read-heavy datasets.

## 4. Architecture overview

```
                        ┌─────────────────────────────────────────┐
                        │                 dedid                   │
   Public read plane    │  ┌───────────┐   ┌──────────────────┐   │
  (unauthenticated,     │  │ Lookup /  │   │  Transparency    │   │
   CDN-cacheable)  ────►│  │ Query API │◄──│  Log (tlog,      │   │
                        │  └───────────┘   │  Merkle)         │   │
                        │  ┌───────────┐   └──────────────────┘   │
   Publisher plane      │  │ Publish / │            │             │
  (authenticated,  ────►│  │ Admin API │            ▼             │
   separate listener)   │  └───────────┘   ┌──────────────────┐   │
                        │        │         │  Checkpoint      │   │
                        │        ▼         │  signer (Ed25519)│   │
                        │  ┌───────────┐   └────────┬─────────┘   │
                        │  │ Postgres  │            │             │
                        │  │ (append-  │            ▼             │
                        │  │  only)    │      external witnesses  │
                        │  └───────────┘   (other nodes, CT-style │
                        └────────────────── witness, dedi.global) ┘

   Layered services (separate deployables, pure API clients of dedid):
   - registrar-svc  : Beckn subscriber lifecycle state machine
   - explorer UI    : public directory browser + in-browser proof verification
   - console UI     : publisher/registrar admin
   - dedi CLI       : publish, verify, monitor
```

### 4.1 Storage model

Postgres, strictly append-only. A record "update" is a new version row; nothing is ever mutated or deleted. **Raw signed payload bytes are the source of truth** (`payload_raw BYTEA`); a derived JSONB column exists only to serve queries (see Addendum A, finding 2). Leaf indices are assigned by a serialized writer under an advisory lock — never by a Postgres sequence (Addendum A, finding 1).

Properties that fall out of the schema:

- **Provenance & history:** the version rows *are* the audit trail. "Record as of time T" is a query (`?as_on=`), not a feature.
- **Deterministic log:** dense `seq` gives a total order for the Merkle tree; the write path is a single serialized transaction.
- **Tombstones, not deletes:** revocation/removal is a new version with a revoked state; the history stays provable.
- **Data-protection policy (v1):** payloads must not contain personal data requiring erasure; crypto-shredding is the future escape hatch if that constraint must be relaxed.

### 4.2 Tamper-evidence: the transparency log

- **Primitive:** Merkle log using `golang.org/x/mod/sumdb/tlog` (the battle-tested package behind the Go checksum database). Leaf = `tlog.RecordHash` over a canonical encoding of `(entry_type, namespace, registry, record_name, version_num, payload_digest, created_by, created_at)`.
- **Checkpoints:** signed tree heads in the C2SP checkpoint format (origin line, decimal size, base64 root hash), signed as a `sumdb/note` with the node's Ed25519 identity key, emitted on a fixed cadence (every N seconds or M entries, whichever first). Checkpoints embed a timestamp via cadence; high-assurance clients enforce a freshness bound (Addendum A, finding 7).
- **Proofs:** any lookup can request `?proof=inclusion` → leaf index + audit path + latest checkpoint. Consistency proofs served between any two tree sizes.
- **Tiles (deferred):** v1 serves proofs computed from Postgres; C2SP tile serving for CDN mirroring lands with the witness work. Evaluate `transparency-dev/tessera` before hand-rolling tiles (Addendum A, finding 5).
- **Witnessing (v2):** checkpoints are cosigned by external witnesses via the C2SP `tlog-witness` protocol — other dedid nodes, a CT-style witness network, or dedi.global itself as one anchor among several.

**Why not a blockchain:** the threat model for a public directory is a *malicious or compromised operator rewriting history*, not Byzantine consensus among mutually distrusting writers. A single-writer log + external witnesses addresses exactly that threat, keeps ops to "a Go binary and Postgres," and preserves offline verifiability for relying parties. CORD anchoring remains compatible — it is simply one more witness.

### 4.3 Two planes

| | Read plane | Publisher plane |
|---|---|---|
| Auth | None (public data) | Publisher keys (detached JWS on writes) + operator RBAC |
| Surface | DeDi lookup/query/versions, proofs, checkpoints | namespace/registry CRUD, record publish, key registration/rotation |
| Listener | `:8080`, CDN in front | `:8443`, private network / mTLS optional |
| Scaling | Read replicas + cache; effectively static | Single writer, low volume |

The node identity key signs **checkpoints only** — read-plane responses are not individually signed; TLS covers transport and proofs cover content (Addendum A, finding 6).

### 4.4 Keys

- **Node identity key** (Ed25519): signs checkpoints.
- **Publisher keys** (per namespace, Ed25519): sign every write; `publisher_kid` recorded per version (publisher plane, M2).
- **Recursive trust:** each namespace's key history is itself published as a DeDi registry on the node (`<ns>/_keys`), so key rotation is discoverable and provable through the same mechanism as everything else.
- **Temporal resolution:** the spec's `?as_on=<timestamp>` and `?version_id=` answer "what was valid at time T" — required for validating signatures on old messages after rotation.

## 5. API surface

### 5.1 DeDi conformance (read plane)

Implement `api/openapi.yaml` from the LFDT lab (v2.0.0) verbatim — see Addendum B for the teardown. Surface: `GET /dedi/lookup/{ns}[/{registry}[/{record}]]` (with `version_id`, `as_on`), `GET /dedi/query/{ns}[/{registry}]` (name/date/state filters, sort, pagination), `GET /dedi/versions/{ns}[/{registry}[/{record}]]`. Responses use the `{message, data}` envelope; errors use `{message, error, code}`. Extensions are additive and namespaced (`?proof=`, `/dedi/log/*`) so a spec-only client sees a conformant node.

**Open question (tracked as risk R1):** the ONIX `dediregistry` plugin was recently migrated to a "new wrapper API," implying the de facto wire contract is what dedi.global serves, which may drift from the LFDT spec. Action: diff the plugin's client code against the spec; where they diverge, serve both shapes behind content negotiation or a compat prefix, and file the divergence upstream.

### 5.2 Query (Beckn discovery path)

The spec's query endpoint filters by name/date/state only. Attribute filtering over payload fields (role, domain, city/coverage, status) — required for gateway discovery — is a namespaced extension backed by the JSONB GIN index (containment only, keyset-paginated by `seq`). Exact-key lookup stays on the GET path and is the latency-critical route.

### 5.3 Publisher plane (M2)

- `PUT /admin/namespaces/{ns}`, `PUT .../registries/{reg}` (with JSON Schema for record payloads; writes validated against it)
- `POST .../records/{id}:publish` (new version; body signed by publisher key)
- `POST .../records/{id}:revoke` (sugar for a revoked-state version)
- Key registration and rotation endpoints.

### 5.4 Freshness / push (pulled forward for Beckn)

- **ETag/If-None-Match everywhere**; small max-age on latest-version reads, immutable on version-pinned reads.
- **Webhooks:** registry-level subscriptions firing on new versions (primary consumer: revocation propagation to caching validators). Postgres outbox table + poller at v1 scale.

## 6. Beckn compatibility layer

Everything Beckn-specific lives *above* the generic node.

### 6.1 What ONIX needs from the registry

Participants register `(subscriber_id, role ∈ {BAP, BPP, BG}, signing public key (Ed25519), encryption public key (X25519), callback URI, domain(s), coverage, status, validity window)`. **Two keys, not one** (Addendum A, finding 3). The adapter performs (a) key lookup by participant ID on every inbound message for Ed25519 signature validation, and (b) routing resolution of callback URLs. Gateways additionally query by role/domain/city.

Mapping: one namespace per network, a `participants` registry with the Beckn subscriber JSON Schema (the LFDT lab ships `schemas/Beckn_subscriber.json`), records keyed by `subscriber_id`.

### 6.2 registrar-svc (subscriber lifecycle)

A separate deployable implementing the Beckn onboarding state machine, using only the public publisher API:

```
REGISTERED ──challenge sent──► UNDER_VERIFICATION ──on_subscribe OK──► INITIATED
INITIATED ──registrar approval──► SUBSCRIBED
SUBSCRIBED ──expiry/revocation──► EXPIRED / REVOKED   (new record versions)
```

- Key-possession challenge via `on_subscribe`: ECDH (registrar X25519 keypair × subscriber `encr_public_key`) → AES-encrypted challenge; correct decryption proves control.
- Every state transition is a new record version — the network's PKI history is a provable log.

### 6.3 Acceptance test

Run the Beckn starter kit's docker-compose network; point both ONIX adapters' registry URL at dedid + registrar-svc instead of `fabric.nfh.global`; the discover → select → init → confirm flow must pass end-to-end with signature validation and dynamic routing, with zero adapter changes.

## 7. Frontends and tooling

All pure API clients — zero privileged paths.

1. **Explorer** (v1): static SPA embedded via `go:embed`; browse + in-browser inclusion-proof verification (WebCrypto Ed25519, with pure-JS fallback).
2. **`dedi` CLI** (v1): `publish`, `get --verify`, `watch`, `keys rotate`, `witness`.
3. **Console** (v2): publisher/registrar admin.

## 8. Conformance suite (upstream contribution)

A standalone repo/package: black-box HTTP tests runnable against any DeDi endpoint (`conformance run --target <url>`), generated from the LFDT OpenAPI specs plus semantic tests the schema can't express (version monotonicity, history immutability, pagination stability). Contributed to the LFDT lab first, before the implementation is announced. Run in CI against dedid and (read-only) against dedi.global; divergences become upstream issues.

## 9. Technology choices

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go 1.24+ | Team fluency; single static binary; tlog ecosystem |
| HTTP | stdlib `net/http` (1.22+ pattern mux) | Boring, zero deps |
| DB access | pgx v5, hand-written queries | Typed enough at this query count; sqlc when surface stabilizes |
| Merkle log | `golang.org/x/mod/sumdb/tlog` + `sumdb/note`, C2SP checkpoint format | Battle-tested; witness-ecosystem interoperable |
| Crypto | stdlib Ed25519 via `sumdb/note` | Minimal surface |
| Frontend | Static SPA, `go:embed` | Single-binary deploys |
| Testing | Integration tests against real Postgres; reference-implementation cross-checks for log math | Log correctness is the crown jewel |
| Packaging | Docker Compose (node+Postgres), Helm later | Self-host first |

## 10. Milestones

| # | Deliverable | Exit criterion |
|---|---|---|
| M0 | Spec teardown + conformance suite skeleton | Suite runs against dedi.global; divergence report filed upstream |
| M1 | Core node: append-only store, tlog, checkpoints, read API | Read API green; inclusion/consistency proofs verified offline |
| M2 | Publisher plane + CLI + explorer | Publish→lookup→verify round-trip via CLI and browser |
| M3 | Beckn layer: attribute query, temporal keys, webhooks, registrar-svc | Starter-kit network passes E2E against dedid (§6.3) |
| M4 | Witness cosigning + console UI | Two-node witness demo; checkpoint divergence detected and alarmed |
| M5 | Public release | Announce with conformance suite upstreamed + Beckn drop-in demo |

## 11. Risks

- **R1 — Spec vs. de facto drift.** dedi.global's wrapper API may be the real contract for ONIX. *Mitigation:* diff the plugin client early (M0); dual-shape serving if needed.
- **R2 — Spec instability.** No releases upstream. *Mitigation:* conformance suite doubles as a spec-change detector.
- **R3 — Governance friction.** *Mitigation:* contribute early and small (M0).
- **R4 — Beckn coupling churn.** *Mitigation:* pin acceptance tests to a specific ONIX release.
- **R5 — Scope creep toward a database.** *Mitigation:* query surface fixed at spec filters + JSONB containment extension.

## 12. Open questions

1. ~~Exact endpoint shapes~~ — resolved, see Addendum B.
2. registrar-svc in-repo vs separate — leaning monorepo until interfaces stabilize.
3. Checkpoint cadence and witness policy defaults (v1 default: 30s / on-demand for proofs; freshness bound recommendation 5 min).
4. Publish node to the LFDT lab vs keep under Flywheel — decide after M0 reception.
5. One global log vs per-namespace logs — v1 ships a single global log; this is a one-way door for namespace portability (a namespace cannot later migrate to another node with proofs intact). Revisit before M5.

---

## Addendum A — Design review findings (2026-07-15)

Accepted as v0.2 changes:

1. **Dense leaf indices.** Postgres `IDENTITY`/sequences leave gaps and commit out of order — unusable as Merkle leaf indices. The write path is one serialized transaction holding an advisory lock, assigning `seq = max(seq)+1`.
2. **Raw bytes are the truth.** JSONB normalizes (key order, duplicates, numbers), breaking digest round-trips. Store `payload_raw BYTEA` (compacted JSON) as the hashed/served source of truth; JSONB is a derived query index. Serve `details` via `json.RawMessage` so bytes survive to the client.
3. **Beckn dual keys.** Subscribers carry a signing key (Ed25519) *and* an encryption key (X25519); the `on_subscribe` challenge is ECDH-derived AES, so registrar-svc needs its own X25519 pair.
4. **Data-protection policy.** Append-only forever ⇒ v1 policy: no personal data requiring erasure in payloads; crypto-shredding as future escape hatch.
5. **Evaluate Tessera** (`transparency-dev/tessera`) before hand-rolling tile storage.
6. **No read-plane response signing.** Checkpoints are the only thing the node key signs; TLS + proofs cover the rest.
7. **Checkpoint freshness bound.** Cadence default 30s; high-assurance clients reject checkpoints older than 5 min (revocation propagation bound).
8. **Keyset pagination by `seq`** internally for stability (spec pagination is page-numbered; ordering must be deterministic).
9. **Global vs per-namespace log** recorded as open question 5.

## Addendum B — LFDT spec teardown notes (M0, 2026-07-15)

From `api/openapi.yaml` (DeDi API v2.0.0) at `LF-Decentralized-Trust-labs/decentralized-directory-protocol@main`:

- **Terminology:** namespace → **registry** → record. Not "directory". Path params: `{namespace}`, `{registry_name}`, `{record_name}`.
- **Read surface (all `security: []`, i.e. public):**
  - `GET /dedi/lookup/{ns}[/{reg}[/{rec}]]` — `?version_id=<string>`, `?as_on=<RFC3339>` (temporal resolution is already in the spec; our proposed `?at=` is unnecessary).
  - `GET /dedi/query/{ns}` — filters `from`, `to` (date-time), `status` ∈ {active, inactive}, `name`, `sort` ∈ {date, status, name, id}, `page`, `page_size`, `as_on`. Returns namespace header fields + `registries: [RegistrySummary]` + totals.
  - `GET /dedi/query/{ns}/{reg}` — same shape, `state` ∈ {live}, returns registry header (incl. `schema`) + `records: [RecordSummary]` + `total_pages`.
  - `GET /dedi/versions/{ns}[/{reg}[/{rec}]]` — `{created_by, created_at, updated_at, total_versions, versions: [string], ttl}` (+ `registry_name`/`schema` at registry level, `schema` at record level).
- **Envelope:** success `{message: string, data: {...}}`; error `{message, error, code}`.
- **Entities:** Namespace `{namespace_id, name, description, digest, meta, version, version_count, created_at, updated_at, created_by, domain, state ∈ [active, archived, revoked], ttl}`; Registry adds `schema`; Record `{record_id, record_name, registry_id, registry_name, namespace_id, namespace, description, digest, details, meta, version, version_count, genesis, created_at, updated_at, created_by, state ∈ [draft, live, suspended, revoked, expired], valid_till, ttl}`.
- **Auth:** Bearer/cookie schemes exist globally but every read endpoint opts out — matches our public read plane.
- **Spec inconsistencies to file upstream:** query `status` enum (active/inactive) doesn't match entity `state` enum (active/archived/revoked); records query `state` enum has only `live`; `versions` items are bare strings with no timestamps; no error code enumeration.
- **Schemas dir:** `Beckn_subscriber.json`, `Beckn_subscriber_reference.json`, `public_key.json`, `membership.json`, `revoke.json` — ready-made registry schemas; Beckn subscriber schema confirms the dual-key model.
- **Not in the spec:** any write/publish surface (publisher plane is implementation-defined), any proof/checkpoint surface (our extension), tiles, webhooks.

## Addendum C — ONIX `dediregistry` wire contract (M3 teardown, 2026-07-16)

Extracted from beckn-onix@e99b8c1 (v1.8.0). Supersedes §6.1's assumptions; resolves risk R1.

- Lookup (signature-validation hot path): `GET {url}/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}` — the middle segment is a hardcoded wildcard meaning "search all registries". dedid implements this via exact-resolve-first, then wildcard fallback (`FindBecknSubscriber`), live records only.
- Node lookup: `GET {url}/lookup/{ns}/{registry}/{record}`; registry metadata: `GET {url}/lookup/{ns}/{registry}` (requires `data.meta`).
- Response contract: `{message, data}` with `data.details.{signing_public_key (std-base64 raw Ed25519), url, type, domain, subscriber_id, encr_public_key}`, `data.network_memberships []string` (enforced client-side against `allowedNetworkIDs` — absent list + configured allowlist ⇒ rejection), optional `data.ttl` seconds (client cache override). Any non-200 ⇒ unknown participant ⇒ ONIX responds 401 NACK.
- NOT used by ONIX: subscribe/on_subscribe (onboarding is out-of-band), registry-based routing (callback URLs come from `context.bpp_uri`), response signing. VC revocation is a bare GET on a credential-embedded lookup URL: 200 ⇒ revoked, 404/410 ⇒ not revoked.
- **Locked constant:** stock ONIX force-injects `dediregistry.url = https://fabric.nfh.global/registry/dedi` from a signature-verified embedded constants file (plugin-manager enforcement on the exact plugin id `dediregistry`; any other configured value fails startup). Consequences: (a) the §6.3 acceptance test requires a patched/forked adapter build or a network-level override; (b) an upstream change request is needed before "change only the registry URL" is honest for stock deployments; (c) importing the plugin's Go package directly bypasses the lock — which is how the contract test works.
