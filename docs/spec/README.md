# Referenced specifications

Both are pinned to a commit and **read-only — never edit these files.** To
update, follow the steps below and review the diff; that diff is the point,
since upstream has no releases to track (design.md R2).

Our conformance position against both is in [`../conformance.md`](../conformance.md).

---

## `lfdt/` — the standard

`LF-Decentralized-Trust-labs/decentralized-directory-protocol`, Apache-2.0.
Explicitly a standard, not a software product.

This is a **git submodule**, not a copy: the upstream repository *is* the
directory, and this repo records which commit of it we build against. So there
is no vendored duplicate to drift, and `git log`/`git diff` inside
`lfdt/` are upstream's real history rather than a series of "refresh the
vendored spec" commits.

- **Pinned commit:** `52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18` (2026-08-04)
- **Tracking branch:** `main` (used by `--remote`; the pin is what builds use)

A plain `git clone` leaves it empty. Clone with `--recurse-submodules`, or in
an existing checkout — and in CI, before any build or test that reads the
spec:

```sh
git submodule update --init --recursive
```

This is the spec design.md §5.1 commits us to implementing. It has two surfaces:

| Path | What it is |
|---|---|
| `api/openapi.yaml` | DeDi API **v2.0.0** (MIT), 8 read paths — lookup/query/versions. What a server exposes. |
| `docs/publishing-dedi-files.md` | The file publication model. How data gets *into* such a server. |
| `schemas/dedi-file.schema.json` | A signed, self-contained registry file. |
| `schemas/dedi-manifest.schema.json` | The manifest at the normative path `/.well-known/dedi.index.json`. |
| `schemas/{Beckn_subscriber,Beckn_subscriber_reference,public_key,membership,revoke}.json` | Reference registry schemas. |
| `docs/trust-pillars.md` | Integrity / Validity / Authenticity. Descriptive prose — no MUST/SHOULD anywhere. |
| `domains.txt`, `examples/` | The crawlable discovery list and worked examples. |

Moving the pin — a deliberate act, reviewed like any dependency bump:

```sh
git -C docs/spec/lfdt fetch origin main
git -C docs/spec/lfdt log --oneline 52e120d..origin/main   # read what changed
git -C docs/spec/lfdt checkout <new-sha>
git add docs/spec/lfdt && git commit                       # records the new pin
```

Then re-run `go test ./conformance/...` and `go generate ./internal/refschemas/...`
before committing. Two things upstream has already done that make this more
than a formality: the reference schema files have been **renamed** (case flips
in both directions — `public_key.json` ↔ `Public_key.json`,
`Beckn_subscriber.json` ↔ `beckn_subscriber.json`), which silently breaks
`refschemas`' `go:generate` line and, on a case-insensitive filesystem, may not
look like it changed anything; and new schemas have appeared that we do not
serve. Update the pin, then fix what it broke — do not do both blind.

---

## `dedi-global/` — the hosted platform API

`nfh-trust-labs/docs`, 73 paths. **This is not the standard.** It is the API of
dedi.global, the hosted implementation operated by Networks for Humanity
Foundation, and it is the file linked from the public GitBook developer docs.

- **Commit:** `751737a8a748652420654cb0e5c265408e8fab50`
- **Vendored:** 2026-08-08

A copy rather than a submodule, unlike `lfdt/`: this is one file out of a
73-path documentation repo we track only as evidence, not a standard we
implement against. A submodule would drag in the whole repo to pin a single
YAML we never build from.

Kept for one reason: design.md **R1** — *"dedi.global's wrapper API may be the
real contract for ONIX. Mitigation: diff the plugin client early; dual-shape
serving if needed."* This file is that contract in machine-readable form, which
is better evidence than reading the plugin source.

Roughly 30 of its 73 paths are account and platform management (register, magic
link, token refresh, API keys, notifications, bulk-upload jobs, CSV, delegates
by email, ownership transfer). Those are outside our v1 non-goal on
multi-tenant SaaS operation and are not parity targets.

Refresh:

```sh
SHA=<new-sha>
curl -sSL -o docs/spec/dedi-global/openAPI.yaml \
  "https://raw.githubusercontent.com/nfh-trust-labs/docs/$SHA/openAPI.yaml"
```

Prose versions of the same docs are on GitBook; appending `.md` to any page URL
returns markdown, and `https://dedi-global.gitbook.io/docs/llms.txt` indexes
every page.
