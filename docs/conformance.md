# Conformance against the DeDi standard

**Standard of record:** `LF-Decentralized-Trust-labs/decentralized-directory-protocol`,
Apache-2.0; `api/openapi.yaml` is DeDi API v2.0.0, MIT.

Tracked as a git submodule at [`spec/lfdt/`](spec/lfdt/), pinned to commit
`52e120d` — every claim below is measured against that pin, not against a
moving `main`. See [`spec/README.md`](spec/README.md) for provenance and how to
move the pin. A fresh checkout needs `git submodule update --init --recursive`
before any of this can be re-verified.

Not to be confused with `nfh-trust-labs/docs/openAPI.yaml` (73 paths, vendored
at [`spec/dedi-global/`](spec/dedi-global/)), which is the **dedi.global hosted
platform** API — an implementation, not the standard. That file matters for a
different reason: it is the de-facto contract ONIX was built against, tracked as
design.md R1. It is not what this document measures.

The standard has **two surfaces**, and they are architecturally different:

1. **The read API** — 8 paths. What a server exposes.
2. **The file publication model** — signed JSON files, a well-known manifest,
   and a crawlable domain list. How publishers get data *into* such a server.

We implement all of surface 1, and as of tasks #43–#48 the producing half of
surface 2: signed DeDi files and the well-known manifest are served, so a
conformant server can now crawl us. What remains unimplemented is the
*consuming* half — `domains.txt` and a crawler that ingests other publishers.

---

## Surface 1 — Read API (`api/openapi.yaml` v2.0.0)

All 8 paths are `security: []` (public). All 8 are implemented.

| Spec path | Status | Notes |
|---|---|---|
| `GET /dedi/lookup/{ns}` | ✅ | `server.go:116` |
| `GET /dedi/lookup/{ns}/{reg}` | ✅ | `server.go:117` |
| `GET /dedi/lookup/{ns}/{reg}/{rec}` | ✅ | `server.go:118` |
| `GET /dedi/query/{ns}` | ✅ | `server.go:119` |
| `GET /dedi/query/{ns}/{reg}` | ✅ | `server.go:120` |
| `GET /dedi/versions/{ns}` | ✅ | `server.go:121` |
| `GET /dedi/versions/{ns}/{reg}` | ✅ | `server.go:122` |
| `GET /dedi/versions/{ns}/{reg}/{rec}` | ✅ | `server.go:123` |

### Parameters

| Endpoint | Spec params | Ours |
|---|---|---|
| lookup ×3 | `version_id`, `as_on` | ✅ both (`lookup.go:17`) |
| query `/{ns}` | `from`, `to`, `status`, `name`, `sort`, `page`, `page_size`, `as_on` | ✅ all (`query.go:13`) |
| query `/{ns}/{reg}` | `from`, `to`, `state`, `name`, `sort`, `page`, `page_size`, `as_on` | ✅ all |
| versions ×3 | none | ✅ |

`sort` — spec enum `[date, status, name, id]`. All four whitelisted in
`store/query.go:38`, anything else is `ErrInvalidFilter` → 400.

### Response bodies

Checked field-by-field against the spec schemas. Envelope is `{message, data}`
as specified.

- **Namespace lookup** — `namespace_id, name, description, digest, meta, version,
  version_count, created_at, updated_at, created_by, domain, state, ttl`. All 13
  present, `dto.go:53`.
- **Registry lookup** — all 14 present incl. `schema`, `dto.go:80`.
- **Record lookup** — all 19 present incl. `genesis`, `valid_till`, `dto.go:108`.
- **Namespace query** — header fields + `registries[]` summaries, all present.
- **Registry query** — header fields + `records[]` + `total_pages`, all present.
- **Versions ×3** — `versions.go`. Correctly differentiated: `registry_name`
  only at registry level, `schema` at registry and record level but not
  namespace, matching the spec exactly.

### State enums

Spec: namespace/registry `state` ∈ `[active, archived, revoked]`; record `state`
∈ `[draft, live, suspended, revoked, expired]`.

We default namespace/registry → `active` and record → `live` (`append.go:96-111`),
and revoke writes `revoked` (`admin.go:397`). **Every value we emit is inside the
spec enum.** We never produce `archived`, `draft`, `suspended` or `expired`;
absence of an optional state is not a divergence.

### Out-of-spec surface (strict view: not in spec ⇒ non-conformant)

The spec sets no `additionalProperties: false`, and `publishing-dedi-files.md`
§1.3 says a server "MAY additionally offer capabilities that static files do not
provide, such as cross-directory search, version history, conditional fetch, and
availability guarantees." That MAY clause sanctions *some* extras — but under a
strict reading everything below is implementation-specific and a spec-only
client or crawler has no defined semantics for any of it.

| Out-of-spec surface | Where | Covered by §1.3 MAY? |
|---|---|---|
| Write API — 9 `/admin/*` routes | `server.go:151-159` | **No.** The spec has no write API; publishing is file hosting. This substitutes for, not extends, the spec's ingestion model. |
| `POST /enrol`, `GET /dedi/delegations/{ns}` | `server.go:128,133` | **No.** `/enrol` moved off the spec's `/dedi/` prefix (task #44); `POST /dedi/enrol` remains registered as a deprecated alias, with a `Deprecation: true` header, until deployed ring nodes upgrade to call `/enrol` directly. |
| `?proof=inclusion` | lookup | **No** — param not in spec. |
| `?domain=` discovery | `query/{ns}/{reg}` | **No** — param not in spec. |
| `network_memberships`, `expired`, `not_yet_valid` | record body, `omitempty` | **No** — fields not in the `Record` schema. |
| `_witness` namespace | `witness.go:28` | **No** — verdicts are visible through spec read endpoints as records no spec defines. Fixed as of task #45: any namespace prefixed `_` now 404s from `/dedi/lookup`, `/dedi/query`, and `/dedi/versions` unless the request carries `?internal=1`, so `_witness` no longer answers a spec-only crawler; the explorer and verify pages keep working by passing that param. Since issue #27 the verdicts are also published properly, at `GET /dedi/witness` — the hiding rule is unchanged, but reading a verdict no longer requires knowing about it. |
| Webhooks / subscriptions | `/admin/*` | **No** — spec freshness is pull + `next_update`. |
| Anchoring | internal only | n/a — not externally visible. |
| `/dedi/log/*` proofs | new paths | Generous reading of "verification/availability"; logs never named. |
| `/dedi/network`, `/dedi/stats`, UI pages, `/healthz` | new paths | Operational surface, outside API scope. |
| `GET /dedi/witness`, `GET /dedi/witness/{target}` | `witnessview.go` | **No** — not in the spec. A read surface for this node's own verdicts about other nodes, added by issue #27 so the claim is not reachable only via `?internal=1`. It sits under `/dedi/` alongside `/dedi/log/checkpoint`, which is the same kind of thing — evidence about the log rather than directory data — and unlike `/enrol` (task #44) it is a read, so it cannot be mistaken for a spec write path. It reads no namespace a conformant client can see and returns nothing a conformant client depends on. |
| Raft replication | internal | **Yes** — "availability guarantees" is named verbatim. |

The two worst offenders were the ones occupying space the spec owns:
`/dedi/enrol` on the spec's path prefix, and `_witness` inside the public
record space. The former is fixed as of task #44 — enrolment now lives at
`POST /enrol`, with `POST /dedi/enrol` kept only as a deprecated alias (it
answers with a `Deprecation: true` header) until every deployed ring node has
upgraded to call the new path. The latter is not theoretical — verified live
2026-08-08: `GET /dedi/lookup/_witness` on dedi-b returns a fully-formed spec
`Namespace` body (`created_by: "witness"`), and `GET /dedi/query/_witness`
lists the verdict registry. A spec-only crawler indexes both as ordinary
directory data.

Absence verified against the spec text, not assumed: `proof` and `domain` never
appear as parameter names anywhere in `openapi.yaml`; `network_memberships`,
`expired` and `not_yet_valid` are not among the 19 `Record` properties
(`openapi.yaml:745-786`); `enrol`, `delegation`, `witness`, `webhook` and
`admin` appear nowhere in the API spec at all.

One mitigating citation: spec §14 "Open questions"
(`publishing-dedi-files.md:497-505`) explicitly lists **multi-key /
delegation** and a **monitor / transparency layer** ("an optional witness that
logs manifest key-changes, to close the host-compromise gap") as open,
unspecified areas. Our delegation and witness planes are therefore
pre-implementations of problems the spec acknowledges but has not yet
standardised — wrong under a strict not-in-spec rule, but aimed at holes the
authors themselves have flagged.

### Nits — not conformance failures, worth knowing

1. ~~**Filter enums are not validated.**~~ Fixed by task #46. `store/query.go`
   now whitelists a `validStates` union — the spec's query enums (`active`,
   `inactive`, `live`) plus the states we actually store (`archived`, `revoked`,
   `suspended`, `expired`, `draft`) — and anything outside it is
   `ErrInvalidFilter` → 400, the same path `sort` already took. The union rather
   than the bare spec enum is deliberate: the spec's own enums are internally
   inconsistent (query `status` `[active, inactive]` cannot match entity `state`
   `[active, archived, revoked]`; Addendum B logged this to file upstream), so
   rejecting stored states would break working queries to satisfy a
   contradiction.
2. **`sort=id` sorts by name** (`query.go:42`), because our version ids are log
   sequence numbers with no per-entity id column. Deterministic and stable, but
   not literally an id ordering.
3. **`version_id` type mismatch** — found by the conformance suite, not by
   reading. `openapi.yaml` types the lookup `version_id` parameter as an
   unconstrained `string`, but our handler 400s anything non-numeric
   ("version_id must be an integer version id"). A conformant client sending a
   value the documented type permits (a UUID, say) is rejected. Left unfixed on
   purpose: the resolution is either tightening the type upstream or loosening
   our handler, and that is a product call. `conformance/` records it as a
   logging, non-failing test rather than asserting either answer.

**Verdict on surface 1: full conformance, now executable.** `conformance/`
asserts it against the pinned spec on every run — see "Conformance suite" below.

---

## Surface 2 — File publication model

This is the part Addendum B (2026-07-15) missed: it tore down `api/` and part of
`schemas/`, and never covered `docs/publishing-dedi-files.md`,
`schemas/dedi-file.schema.json`, `schemas/dedi-manifest.schema.json`, or
`domains.txt`.

In the standard's model a publisher does **not** call a write API. They sign a
JSON file and host it themselves; servers crawl and aggregate. Authority is
anchored in TLS + domain control, explicitly analogous to `did:web`.

| Artifact | Spec | Ours |
|---|---|---|
| DeDi file | `dedi.<registry>.json`, recommended at `/dedi/` | ✅ emitted at `GET /dedi-files/{namespace}/dedi.{registry}.json` (`internal/dedifile`, `internal/api/dedifile.go`) |
| Manifest | `/.well-known/dedi.index.json` — **normative path** (RFC 8615) | ✅ served at `GET /.well-known/dedi.index.json` (`internal/api/dedifile.go`) |
| Discovery list | `domains.txt`, one domain per line, `# head:` timestamp | ❌ not published or consumed |
| Crawler | server fetches listed domains' manifests | ❌ none |

Producer side only: this node now signs and serves its own DeDi files and
manifest, so a standard-conformant server can ingest our directory. We do not
consume other publishers' files (no `domains.txt` publication, no crawler) —
that half remains out of scope. See `docs/file-publication.md` for the
producer implementation and how a third party verifies our output.

`/dedi-files/` rather than the spec's RECOMMENDED `/dedi/` directory: `/dedi/`
is already this node's API prefix (`/dedi/lookup`, `/dedi/query`, ...), and
the filename/path conventions are non-normative — only the manifest's
`/.well-known/` path is fixed. The manifest's `files[].url` is authoritative
regardless of the path chosen.

**`dedi-file.schema.json` required fields:** `dedi_version`, `type`, `source_url`,
`next_update`, `publisher{domain, key}`, `namespace`, `registry{name, schema,
state, updated_at}`, `records`, `proof{verification_method, canonicalization, jws}`.

**`dedi-manifest.schema.json` required fields:** `dedi_version`, `domain`,
`keys[]` (JWK: `kid, kty, crv, x, y, n, e`), `updated_at`, `next_update`,
`files[]`, `proof`.

**Revocation is published, not implied by absence** (task #49). The file schema
has no per-record state — lifecycle lives at the registry level and, for
individual records, in a *negative registry* (§5.1). A revoked record is
therefore not dropped from its registry's file and forgotten; it is emitted in
a per-namespace `revocations` registry against the spec's own `revoke.json`
schema, as `{revoked_id, reason}`. Ids are qualified `{registry}/{record}`
because record names are unique only within a registry while all of a
namespace's revocations share one list, and a duplicate `record_name`
invalidates the whole file. If an operator already runs a registry called
`revocations`, theirs is left untouched and ours becomes `dedi-revocations`.

**Verification, per the spec (5 steps):** schema validation → offline JWS
integrity check (JCS canonicalization) → one network fetch of the manifest to
confirm the key is still in `keys[]` → freshness against `next_update` →
`registry.state == "live"`. Steps 1–2 are location-independent.

### Why this matters more than it looks

**The standard's unit of interoperability is the file, not the API.** A
conformant DeDi server ingests other directories by crawling manifests. We now
implement the producer side — a signed DeDi file per registry plus a signed
manifest, a serialization of data we already hold — so a standard-conformant
server can crawl and ingest our directory. What remains is the discovery half:
we do not publish ourselves to a `domains.txt` list, and we do not run a
crawler that ingests *other* publishers' files into this node.

---

## Surface 3 — Reference schemas

`schemas/` ships `Beckn_subscriber.json`, `Beckn_subscriber_reference.json`,
`public_key.json`, `membership.json`, `revoke.json`.

All five are now bundled (task #47). `internal/refschemas` embeds them via
`go:embed` from a copy carrying its provenance (spec commit `52e120d`) plus a
`go:generate` line to refresh from `docs/spec/lfdt/schemas/`. A registry is
created against one by name — `"schema": "builtin:public_key"` — which
`resolveBuiltinSchema` expands to the full schema before storage, so the stored
registry is indistinguishable from one where an operator pasted it by hand, and
an unknown builtin name is a 400 at creation. Two nodes claiming a `public_key`
registry now run the same shape by construction. See
[`reference-schemas.md`](reference-schemas.md).

---

## What the standard does *not* define

Everything here is ours by necessity, not by divergence:

| Area | Standard | Ours |
|---|---|---|
| Write/publish API | none — publishing is file hosting | `/admin/*`, signed requests |
| Transparency log, inclusion/consistency proofs | none | Merkle log, C2SP checkpoints |
| Witnessing | none | `internal/witness`, `_witness` ns |
| Ledger anchoring | none | `internal/anchor`, CORD adapter |
| Push notification | none — freshness is `next_update`, a pull/expiry model | signed webhooks |
| Delegation | none — authority is domain control | cryptographic parent/child enrolment |
| Replication / HA | none | Raft |

Two of these deserve a note rather than a row:

**Proofs are not simply absent from the standard.** It has a full verification
story (signatures, key discovery via manifest, revocation by key removal,
freshness). What it lacks is any notion of **append-only history**: a publisher
can silently rewrite yesterday's file *and* yesterday's manifest and nothing
detects it. Our log is complementary to their model, not a parallel invention —
which makes it a cleaner upstream contribution than it would be otherwise.

**`docs/trust-pillars.md` is entirely descriptive.** Integrity, Validity,
Authenticity, defined in prose with no MUST/SHOULD/SHALL anywhere, no
transparency logs, no witnessing, no multi-party verification.

---

## Conformance suite

Built as [`conformance/`](../conformance/) (task #48), a separable package per
design.md Goal #5. It parses the vendored `openapi.yaml` at test time — there is
no hand-copied route list anywhere in it, so the spec file is the only source of
truth — spins the handler up in-process against seeded fixtures, and asserts:

- every spec path resolves (200, not 404) with fixture names substituted for
  path params;
- every documented query parameter is accepted, using the spec's own enum value
  where an enum exists;
- every `required` property of the response component schema is present in
  `data`, walked recursively;
- the envelope is `{message, data}`.

`TestAllSpecPathsAreClassified` is R2's spec-change detector: refresh the
vendored spec, and any new path fails the build until a maintainer classifies
it. The read-API verdict above is therefore no longer a code-reading result.

It earned its keep immediately by finding the `version_id` type mismatch in
nit #3 — a divergence three careful readings of the same YAML had missed.

Still worth adding: differential testing against `api.dedi.global`. The read
endpoints need no auth, so identical requests can be fired at both and the JSON
shapes diffed — which would test us against the implementation ONIX was built
against, not just against the standard (design.md R1).

---

## Summary

| | Implemented | Not implemented | Out of spec (strict: wrong) |
|---|---|---|---|
| Read API (8 paths) | 8 | 0 | 2 params + 3 body fields grafted on |
| File publication model | 2 of 4 — file + manifest | `domains.txt`, crawler | — |
| Reference schemas | 5 of 5 bundled | 0 | — |
| Conformance suite | 1 | 0 | — |
| Spec-owned space | — | — | cleared: `/enrol` moved, `_witness` hidden behind `?internal=1` |

The gap was never correctness — it was that we had built the server half of a
two-halved standard. The producing half of the other now exists: we emit signed
files a conformant server can crawl. What is left is consumption (ingesting
other publishers) and the extensions we chose deliberately, which remain
out-of-spec by definition and are argued for above rather than hidden.
