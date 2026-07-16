# /docs page expansion — design

**Date:** 2026-07-16
**Status:** approved (user), delivered as PR for agent review

## Goal

The public `/docs` page explains *what* the node is but links to nothing: no protocol
spec, no API reference, no way for a reader to run their own node. Expand it so a
visitor can (a) call the API, (b) self-host, and (c) find the specs this node implements.

## Scope (user-selected)

1. **API reference** — table of the actual routes in `internal/api/server.go`, the
   `?proof=inclusion` / `?version_id=` / `?as_on=` params, one live example curl.
2. **Self-host quickstart** — clone → `make keygen` → `docker compose up -d` →
   checkpoint curl, plus the optional seed line. Repo linked normally (it is going
   public soon — user decision).
3. **Protocol & references** — verified links only: LFDT DeDi spec
   (github.com/LF-Decentralized-Trust-labs/DeDi), dedi.global, Beckn protocol,
   beckn-onix (+ `dediregistry` plugin path), C2SP tlog-checkpoint & signed-note,
   RFC 6962, Go `sumdb/tlog`.

Out of scope: a separate verify-it-yourself guide (deferred), multi-page docs,
generated/OpenAPI docs.

## Approach

Grow the single embedded page `internal/api/static/docs.html` (approach A of three
considered; B = multi-page split, C = generated docs — both rejected as YAGNI at this
volume). No routing or Go changes. Existing intro, both mermaid diagrams, and the
test-cases table stay as-is; the three new sections append after the witness note.

## Testing

Extend `TestDocsServed` in `internal/api/static_test.go` to assert the new sections
render through the served page (API table, quickstart, each external link) — consistent
with the acceptance philosophy (exercise via the HTTP surface, not internals).

## Deploy

Not in this PR. After merge, the prod compose redeploy
(`docker compose -f docker-compose.prod.yml up -d --build`) picks it up.
