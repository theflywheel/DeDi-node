# Conformance against the DeDi standard

**Standard of record:** the LF Decentralized Trust
[decentralized-directory-protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/tree/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18)
repository, pinned at commit `52e120d`; its
[`api/openapi.yaml`](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/blob/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/api/openapi.yaml)
is DeDi API v2.0.0. Every claim below is measured against that pin, not
against a moving `main`. The source tree vendors it as a git submodule at
`docs/spec/lfdt`.

Not to be confused with the dedi.global platform's own OpenAPI document
(73 paths), which describes a hosted implementation, not the standard. It
matters because ONIX was built against it, but it is not what this page
measures.

The standard has two surfaces:

1. **The read API**: 8 paths a server exposes.
2. **The file publication model**: signed JSON files and a well-known manifest
   that publishers host, and servers crawl.

This node implements both: all 8 read paths, signed files and manifest as a
publisher, and a crawler as a server. [Spec gaps](spec-gaps.md) is the other
half of this account: what the standard asks for that this node does not do.

## How to run it

### Against any live node: the published suite

[dedi-conformance](https://github.com/theflywheel/dedi-conformance) is a
standalone suite that only issues GETs and needs no credentials, so it is safe
against production. It does not create data, so you tell it where yours is, in
a manifest:

```json
{
  "base_url": "https://dedi.beckn.try-dough.com",
  "publisher_domain": "dedi.beckn.try-dough.com",
  "profiles": ["core", "versioning", "publication", "beckn"],
  "fixtures": {
    "namespace": "beckn-testnet",
    "registry": "subscribers.beckn.one",
    "record": "weather.theflywheel.in",
    "record_with_versions": "mandi.theflywheel.in",
    "revoked_record": "bpp.example.com",
    "absent_namespace": "no-such-namespace-9f3a"
  }
}
```

```sh
docker run --rm -v "$PWD:/work" flywheelai/dedi-conformance:v0.0.1 \
  --manifest /work/dedi-conformance.json
```

Profiles are claimed, not assumed: **core** (the 8 endpoints, envelope, enums,
404s), **versioning** (history, `as_on`, a revoked record staying resolvable),
**publication** (the signed manifest and files, verified per §7.3 of
[publishing-dedi-files.md](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/blob/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/docs/publishing-dedi-files.md)),
and **beckn** (the `Beckn_subscriber` record shape). Exit status is 0 when
every claimed profile passed; `--format junit` or `--format json` for CI.

### In-process: the `conformance/` module

The source tree also carries `conformance/`, a separate Go module so the node
itself builds without it. It parses the vendored `openapi.yaml` at test time
(no hand-copied route list), runs the node's handler in-process against seeded
fixtures, and asserts every spec path resolves, every documented parameter is
accepted, every `required` response property is present, and the envelope is
`{message, data}`. `TestAllSpecPathsAreClassified` fails the build when the
pinned spec grows a path nobody has classified. `TestAgainstTheStandardSuite`
runs the published suite above against that in-process node (claiming core,
versioning and beckn; publication needs a real TLS domain).

```sh
git submodule update --init
cd conformance
TEST_DATABASE_URL='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable' go test -count=1 ./...
```

It needs a throwaway Postgres: the tests truncate it.

## Current results

Measured on 3 October 2026, image `flywheelai/dedi-node:sha-69bffe0`:

| Run | Result |
|---|---|
| Published suite v0.0.1 against `https://dedi.beckn.try-dough.com` | **22/22**: core 8, versioning 5, publication 7, beckn 2 |
| `conformance/` module | 5 tests and 21 subtests, all pass |
| Read API paths implemented | **8 of 8** |

## Surface 1: the read API

All 8 paths are public (`security: []`) and all 8 are implemented, with every
parameter the spec lists:

| Endpoint | Spec parameters |
|---|---|
| lookup ×3 | `version_id`, `as_on` |
| query `/{ns}` | `from`, `to`, `status`, `name`, `sort`, `page`, `page_size`, `as_on` |
| query `/{ns}/{reg}` | `from`, `to`, `state`, `name`, `sort`, `page`, `page_size`, `as_on` |
| versions ×3 | none |

Response bodies carry every property the spec's schemas declare, in the
`{message, data}` envelope; errors use `{message, error, code}`. Every state
value the node emits is inside the spec's enums (`active` / `revoked` for
namespaces and registries, `live` / `revoked` for records).

Where the node is stricter or looser than the text, deliberately:

- **Unknown, repeated or unparseable query keys are a `400`.** The spec does
  not say what to do with them; ignoring them answered `?versionId=2` with the
  latest version (#65, #73). See [API](api.md#strict-query-parameters).
- **`version_id` is an unconstrained string in the spec**, and this node's ids
  are log sequence numbers, so a non-numeric one is a `404` ("no such
  version"), not a `400`.
- **`as_on`, `from` and `to` are `date-time` in this spec and `date` in the
  dedi.global one**, so both are accepted: RFC 3339, or `YYYY-MM-DD` read as a
  whole UTC day (#69).
- **`sort=id` sorts by name**, because there is no per-entity id column apart
  from the name.
- **Filter values are validated**: `status`/`state` must be a spec value or a
  state the node stores, else `400`. The spec's own enums disagree with each
  other (query `status` is `[active, inactive]`, entity `state` is
  `[active, archived, revoked]`), so a union is accepted.

### Extensions, under a strict reading

The spec sets no `additionalProperties: false`, and publishing-dedi-files.md
§1.3 lets a server "additionally offer capabilities that static files do not
provide, such as cross-directory search, version history, conditional fetch,
and availability guarantees". Some extensions fit that; under a strict
not-in-spec-means-non-conformant reading, these are all non-standard:

| Extension | Notes |
|---|---|
| Write API: 13 signed `/admin` routes | The spec has no write API; publishing is file hosting. This substitutes for its ingestion model rather than extending it. |
| `POST /enrol`, `GET /dedi/delegations/{ns}` | Enrolment moved off the spec's `/dedi/` prefix; `POST /dedi/enrol` remains as a deprecated alias with a `Deprecation: true` header. |
| `?proof=inclusion`, `?include_revoked`, `?domain=`, `?internal=` | Parameters the spec does not name. |
| `network_memberships`, `expired`, `not_yet_valid`, `version_tag` on records | Fields outside the `Record` schema. |
| `/dedi/log/*`, `/dedi/witness`, `/dedi/network`, `/dedi/stats`, `/healthz`, `/metrics`, the HTML pages | New paths. Logs and proofs are a generous reading of "verification"; the rest is operational. |
| Webhooks | The spec's freshness model is pull plus `next_update`. |
| `_`-prefixed bookkeeping namespaces | Hidden from lookup, query and versions unless `?internal=1`, so a spec-only crawler does not index them. |
| Raft replication | Fits "availability guarantees". |

Spec §14 ("Open questions") lists multi-key delegation and an optional
transparency monitor as open areas. The delegation and witness planes are
early implementations of problems the authors have flagged but not yet
standardised.

## Surface 2: file publication

| Artifact | Spec | This node |
|---|---|---|
| DeDi file per registry | `dedi.<registry>.json` | served at `/dedi-files/{ns}/dedi.{registry}.json` ([file publication](file-publication.md)) |
| Manifest | `/.well-known/dedi.index.json`, normative path | served there |
| Crawler | a server fetches publishers' manifests and files | `DEDI_CRAWL_DOMAINS` ([crawler and mirror](crawl-mirror.md)) |
| Discovery list | `domains.txt` | not consumed, and this node is not listed in one |

`/dedi-files/` rather than the spec's recommended `/dedi/` because `/dedi/` is
already the API prefix; only the manifest path is normative, and the
manifest's `files[].url` is what a verifier follows. Revocation is published,
not implied by absence: revoked records are listed in a per-namespace
revocations registry using the spec's `revoke.json` shape.

## Surface 3: reference schemas

All five of the spec's schemas are built in and selected by name
(`"schema": "builtin:public_key"`), so two nodes claiming a `public_key`
registry enforce the same shape. See [reference schemas](reference-schemas.md).

## What the standard does not define

| Area | Standard | This node |
|---|---|---|
| Write API | none: publishing is file hosting | signed `/admin` routes |
| Append-only history | none: a publisher can rewrite yesterday's file and manifest undetectably | Merkle log, inclusion and consistency proofs |
| Witnessing | none | `/dedi/witness` |
| Ledger anchoring | none | CORD adapter |
| Push | none: `next_update` is pull | signed webhooks |
| Delegation | none: authority is domain control | parent/child enrolment |
| Replication | none | Raft |

The standard has a full verification story for a single snapshot: signatures,
key discovery through the manifest, revocation by key removal, freshness. What
it lacks is any notion of append-only history, which is what the log adds. It
is complementary to the standard rather than a parallel invention.
