# Vendored specifications

Reference copies, pinned to a commit. **Read-only — never edit these files.**
To update, re-run the commands below with a newer SHA and review the diff; that
diff is the point of vendoring, since upstream has no releases to track
(design.md R2).

Our conformance position against both is in [`../conformance.md`](../conformance.md).

---

## `lfdt/` — the standard

`LF-Decentralized-Trust-labs/decentralized-directory-protocol`, Apache-2.0.
Explicitly a standard, not a software product.

- **Commit:** `52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18`
- **Committed:** 2026-08-04
- **Vendored:** 2026-08-08

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

Refresh:

```sh
SHA=<new-sha>
curl -sSL "https://codeload.github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/tar.gz/$SHA" \
  | tar -xz --strip-components=1 -C docs/spec/lfdt
```

---

## `dedi-global/` — the hosted platform API

`nfh-trust-labs/docs`, 73 paths. **This is not the standard.** It is the API of
dedi.global, the hosted implementation operated by Networks for Humanity
Foundation, and it is the file linked from the public GitBook developer docs.

- **Commit:** `751737a8a748652420654cb0e5c265408e8fab50`
- **Vendored:** 2026-08-08

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
