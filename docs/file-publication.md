# File publication

This node implements the producer half of the DeDi standard's file
publication model ([publishing-dedi-files.md](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/blob/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/docs/publishing-dedi-files.md)): it signs
and serves a DeDi file per registry, plus a signed manifest, so a
standard-conformant DeDi server can discover and ingest this node's data by
crawling files instead of calling `/dedi/lookup` and `/dedi/query`.

This is additive. The existing API (`/dedi/lookup`, `/dedi/query`,
`/dedi/versions`) is unchanged and remains the primary way this node's own
console, webhooks, and delegation flows read and write data. File publication
is a second, static-file projection of the same underlying log, built from
the log and byte-identical until the log or the freshness window moves.

Consuming *other* publishers' files is the crawler's job, off unless
`DEDI_CRAWL_DOMAINS` is set; see [crawler and mirror](crawl-mirror.md).
Namespaces it mirrors are never re-published here, so crawled data is not
re-signed under this node's key.

## Routes

| Route | Serves |
|---|---|
| `GET /.well-known/dedi.index.json` | the signed manifest — every registry this node currently publishes |
| `GET /dedi-files/{namespace}/dedi.{registry}.json` | one signed DeDi file: a registry and its current records |

Both are `Content-Type: application/json`, carry an `ETag` derived from the
response body's own digest (so `If-None-Match` gets a `304`), and
`Access-Control-Allow-Origin: *` (spec §5.2 — the data is public and a
browser-based verifier needs the header to read it cross-origin at all).

The spec's path conventions (§5.2) are RECOMMENDED, not normative — only the
manifest's `/.well-known/` path is fixed (RFC 8615). This node uses
`/dedi-files/` rather than the spec's suggested `/dedi/` directory because
`/dedi/` is already this node's API prefix; a verifier never needs to know
this convention, because the manifest's `files[].url` is what it actually
follows.

Namespaces beginning with `_` (this node's own bookkeeping, e.g. `_witness`)
are excluded from both the manifest and direct file requests — the same rule
`internal/api/internal_ns.go` already applies to the read API.

A record whose current state is `revoked` is omitted from its registry's
file, and listed instead in a per-namespace `revocations` registry (or
`dedi-revocations`, if the operator already has one called `revocations`),
as `{revoked_id, reason}` against the spec's `revoke.json` schema. The spec
models per-record removal through negative registries (§5.1) rather than a
per-record status field, so a revocation is published, not implied by a
record's absence.

## Signing

Every file and the manifest are signed with **this node's existing identity
key** — the same Ed25519 key that signs transparency-log checkpoints
(`internal/checkpoint`) and webhook push deliveries. No second signing
credential is minted or distributed: a relying party that already trusts this
node's checkpoints needs nothing new to trust its DeDi files.

- **Key source**: `internal/dedifile.SignerFromNodeKey` decodes the node's
  `golang.org/x/mod/sumdb/note`-format private key (the same string the
  `dedid keygen` / `DEDI_KEY` / `DEDI_KEY_FILE` paths already produce) into a
  raw Ed25519 private key.
- **`kid` derivation**: `"node-" + hex(sha256(pubkey))[:16]` — derived from
  the public key itself, not from the note key's name (`DEDI_ORIGIN`). This
  keeps the `kid` stable across an operator renaming the node's origin, and
  makes it change automatically if the key is ever rotated.
- **Public key advertisement**: the manifest's `keys[]` carries the node's
  public key as an RFC 7517 JWK — `{"kid": ..., "kty": "OKP", "crv":
  "Ed25519", "x": base64url(pubkey)}` (RFC 8037 §2) — and every DeDi file
  embeds the same JWK in `publisher.key`.
- **Canonicalization**: JCS (RFC 8785), via `github.com/gowebpki/jcs`, over
  the document with the `proof` field structurally removed (not merely
  zeroed — see `internal/dedifile/sign.go`'s `fileNoProof` /
  `manifestNoProof`, which avoids round-tripping record payloads through a
  generic `map[string]any` and risking JSON-number reformatting).
- **Signature**: a compact, detached JWS (RFC 7515 + RFC 7797 `b64:false`),
  algorithm `EdDSA`. The signing input is
  `base64url(header) + "." + canonical_payload_bytes`, and the JWS string
  omits the payload segment: `base64url(header) + ".." + base64url(sig)`,
  matching the example in the spec's worked examples
  (`docs/spec/lfdt/examples/`).

Publisher identity (`publisher.domain` / `manifest.domain`) comes from the
node's configured public origin (`Server.PublicURL`, the same
`DEDI_PUBLIC_URL` a delegated child is told to call back on) and falls back to
the request's own `Host` header when unset — e.g. for a node reached directly
without a reverse proxy in front of it.

## How a third party verifies our output

Follow [publishing-dedi-files.md](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/blob/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/docs/publishing-dedi-files.md) §7.3 exactly; nothing about this
node's output requires special-casing:

1. **Fetch** `GET https://<this-node>/.well-known/dedi.index.json`.
2. **Shape-check** it against `schemas/dedi-manifest.schema.json`.
3. **Offline integrity** — canonicalize the manifest minus `proof` with JCS,
   verify `proof.jws` against the key in `proof.verification_method` /
   `manifest.keys[]`.
4. **Fetch** any `files[].url` you care about (or resolve it via `GET
   /dedi-files/{namespace}/dedi.{registry}.json` directly).
5. **Shape-check** that file against `schemas/dedi-file.schema.json`, then
   repeat step 3 against its own embedded `publisher.key`.
6. **Authenticity** — confirm the file's `publisher.key` (by `kid`) appears
   in the manifest's `keys[]` fetched in step 1. Both files and the manifest
   currently share this node's one identity key, so this always holds unless
   the key has since been rotated.
7. **Freshness** — reject if `now > next_update` on either document.
8. **Registry state** — treat `registry.state == "inactive"` as
   not-authoritative.

`internal/dedifile.VerifyFile` and `VerifyManifest` implement steps 3/5 in
Go, and `internal/dedifile/schema_test.go` checks this node's own output
against the vendored spec schemas on every test run, so a regression here
fails CI rather than a third party's crawl.
