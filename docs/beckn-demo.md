# Running Beckn against a self-hosted DeDi node

This is the runbook for demonstrating that **dedid** — an independent, self-hostable DeDi
registry — is a drop-in for the registry that Beckn ONIX expects (normally `fabric.nfh.global`).

It has two levels:

- **Level 1 — the contract test.** Verified and reproducible today: a real ONIX registry
  client talks to a real `dedid` over HTTP and every call succeeds. This is the honest,
  automated proof of compatibility. *(Green in CI.)*
- **Level 2 — the full network E2E.** Standing up the Beckn starter kit's
  `discover → select → init → confirm` flow with dedid serving every lookup. This is
  **documented but not yet executed** — see the status banner on that section.

Background on *what* ONIX asks the registry for (URL shapes, response fields, the
`subscribers.beckn.one` wildcard, the network-membership check) is in
[`design.md` Addendum C](design.md). This doc is the *how to run it*.

---

## Level 1 — The contract test (works today)

### What it proves
The only real open-source DeDi client that exists is the `dediregistry` plugin inside
[beckn-onix](https://github.com/beckn-one/beckn-onix) (MIT, `v1.8.0`). The contract test
imports that exact client package, points it at a live `dedid`, and exercises the calls
ONIX makes on every Beckn message:

- **subscriber key lookup** (`Lookup`) — the signature-validation hot path,
  `GET {base}/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}`
- **node lookup** (`LookupNode`) — `GET {base}/lookup/{ns}/{registry}/{record}`
- **registry metadata** (`LookupRegistry`) — `GET {base}/lookup/{ns}/{registry}`
- **network-membership enforcement** — accepted when `allowedNetworkIDs` intersects the
  record's `network_memberships`, rejected (401) when it doesn't
- **unknown participant** — any non-200 from the registry ⇒ the client errors ⇒ ONIX NACKs

The node is exercised **only** through its own CLI (`dedid keygen|serve|seed`) and the ONIX
client — no test reaches inside dedid. That is the whole point: if the wire is right, ONIX
is happy.

### Run it
```bash
# needs a throwaway Postgres — do NOT use a prod DB (the suite truncates)
docker run -d --name dedi-test-pg -e POSTGRES_USER=dedi -e POSTGRES_PASSWORD=dedi \
  -e POSTGRES_DB=dedi -p 15433:5432 postgres:16-alpine
export TEST_DATABASE_URL='postgres://dedi:dedi@localhost:15433/dedi?sslmode=disable'

make contract-test
```
Expected: all 6 subtests pass. The target builds `bin/dedid`, boots it, seeds it via
`dedid seed`, and drives the ONIX client against it. Source: `test/onix-contract/`.

### The identities
The seed (`test/onix-contract/testdata/beckn-seed.json`) uses the **starter kit's own
committed testnet identities**, so a demo and the real kit line up 1:1:

| subscriber_id | role | key_id (= dedid record name) | signing key (base64 Ed25519) |
|---|---|---|---|
| `bap.example.com` | BAP | `76EU7LZ7gfqj13dWDKR1Uitnim11mCoxWBPdzLxUpAMBPVdANKgyFM` | `g/3swjI93IhZ0SScrVZapeLjU+W0AeiSid3LViYZJFo=` |
| `bpp.example.com` | BPP | `76EU7ofwRCF1aobQkShARrf1PAUsNpHqWUJoynPu9w45YFKmzqaPmy` | `CqVy97DW45bcZPPrWIYGe2ldl9C93NFeVciiAEYsvR0=` |

All under namespace `beckn-testnet`, registry `subscribers.beckn.one`, with
`network_memberships: ["beckn.one/testnet"]`. The ONIX `keyId` from the adapter config
becomes the dedid **record name**; the lookup finds it by `(subscriber_id, key_id)`.

### The live reference node
A public instance is already running with exactly this data:

- Explorer / registry browser: **https://dedi.proto.theflywheel.in/**
- ONIX-shaped lookup:
  `https://dedi.proto.theflywheel.in/dedi/lookup/bpp.example.com/subscribers.beckn.one/76EU7ofwRCF1aobQkShARrf1PAUsNpHqWUJoynPu9w45YFKmzqaPmy`
- Signed checkpoint: `https://dedi.proto.theflywheel.in/dedi/log/checkpoint`

For an ONIX adapter, the registry **base URL** to configure is
`https://dedi.proto.theflywheel.in/dedi` (the client appends `/lookup/...` itself).

---

## Level 2 — Full Beckn network E2E (starter kit)

> **STATUS: documented, NOT yet executed.** The steps below are the plan derived from
> reading `beckn/starter-kit` and `beckn/beckn-onix`; they have not been run end to end.
> Treat this as the procedure to follow, not a verified transcript. When it is run, replace
> this banner with the results.

### The one hard blocker: the locked registry URL
A **stock** ONIX adapter will not talk to a self-hosted registry by configuration alone.
`dediregistry.url` is a *signed, embedded "locked Beckn constant"*
(`pkg/beckndefaults/beckn-constants.yaml`, enforced by `pkg/plugin/manager.go` on the exact
plugin id `dediregistry`) fixed to `https://fabric.nfh.global/registry/dedi`. Setting any
other `url` **fails adapter startup**. The starter kit's YAML doesn't even expose a `url`
key. So "change only the registry URL" (the naive drop-in story) is impossible unmodified.

Bypass options, best first:
1. **Fork/patch beckn-onix** — env-gate the locked constant (e.g. honor a
   `DEDI_REGISTRY_URL_OVERRIDE`), or re-sign a `beckn-constants.yaml` pointing at dedid, then
   build the adapter image locally. This is the clean, demonstrable path and the basis of the
   upstream PR.
2. **Differently-named plugin** — register `selfhostedregistry` implementing the identical
   `RegistryLookup`/`RegistryMetadataLookup` interface. The lock matches only the literal id
   `dediregistry`.
3. **Network-level** (fallback, hacky) — make the ONIX container resolve `fabric.nfh.global`
   to dedid with a matching TLS cert (DNS + CA trust inside the container).

### Procedure (fork approach)
Requires a Docker host. Repos are staged on `mh-iterations` at `/opt/dedi-node/beckn/`.

1. **Fork beckn-onix, patch the URL lock** so it accepts
   `https://dedi.proto.theflywheel.in/dedi` (or a local dedid). Verify whether the base
   should include `/dedi` or `/registry/dedi` — the client builds `{url}/lookup/...`, and
   dedid serves `/dedi/lookup/...`, so `.../dedi` is correct. (If you must keep the literal
   `/registry/dedi` path, add an nginx rewrite `/registry/dedi/... → /dedi/...` in front of
   dedid.)
2. **Build the patched adapter image** locally as `beckn-onix:latest` (the kit's
   `docker-compose-generic-local.yml` expects that tag).
3. **Seed dedid** with the kit's identities. The public node is already seeded (see Level 1
   table); for a local dedid, `dedid seed -file test/onix-contract/testdata/beckn-seed.json`.
   Cross-check that the `keyId` values in
   `beckn/starter-kit/generic-devkit/config/generic-{bap,bpp}.yaml` match the record names.
4. **Stand up the starter kit**:
   `beckn/starter-kit/generic-devkit/install/docker-compose-generic-local.yml` brings up the
   caddy router, redis, `onix-bap` (:8081), `onix-bpp` (:8082), `sandbox-bap` (:3001),
   `sandbox-bpp` (:3002). Config is bind-mounted from `generic-devkit/config/`.
5. **Mind the two non-registry external deps** the kit hardcodes (swapping the registry does
   NOT fix these): Catalog Service `https://fabric.nfh.global/beckn/catalog`
   (`routing-BPPCaller.yaml`) and Discovery Service `https://34.93.165.42.sslip.io/beckn`
   (`routing-BAPCaller.yaml`). `publish` needs the Catalog service; `discover` needs the
   Discovery service. Keep the hosted ones, or stub them, for a self-contained demo.
6. **Drive the flow** with the kit's Postman collections
   (`generic-devkit/postman/`) via `newman` (`npm i -g newman`): BPP `publish`, then BAP
   `discover → select → init → confirm`.

### Pass criteria
- Each action returns `ACK` and its `on_*` callback lands in the sandbox logs
  (`docker logs -f sandbox-bap`).
- Signature validation succeeds at every hop (no `validateSign` errors in the ONIX logs).
- **dedid's access log shows the lookups** — proof dedid was actually in the loop
  (`docker compose -f /opt/dedi-node/DeDi-node/docker-compose.prod.yml logs dedid`, or the
  local node's logs).

### Negative test (do this — it's the real proof)
Revoke the BPP record in dedid, then rerun `confirm`. Revocation in dedid = append a new
record version with `state: revoked` (the Beckn wildcard search returns **live records
only**, so the lookup then 404s). ONIX must respond **401 NACK**. This proves there is no
silent fallback to the real `fabric.nfh.global` — the demo is genuinely served by dedid.

---

## Upstream contributions this unlocks

- **beckn-onix:** propose making the locked registry URL overridable. The current lock makes
  sovereign / self-hosted Beckn networks impossible with a stock adapter; the working dedid
  demo is the evidence. (This is the strategically important PR.)
- **beckn/starter-kit:** its two shipped READMEs contradict each other on the registry URL
  (`fabric.nfh.global/registry/dedi` vs `api.dev.beckn.io/registry/dedi`) and neither matches
  the actual YAML (which has no `url` key); the compose pulls an **untagged**
  `fidedocker/onix-adapter`. Doc + version-pinning fixes.

---

## Quick reference

| Thing | Value |
|---|---|
| Registry base URL for ONIX | `https://dedi.proto.theflywheel.in/dedi` |
| Lookup shape | `GET {base}/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}` |
| Response | `{message, data}`; `data.details.{signing_public_key, url, type, domain, subscriber_id, encr_public_key}`; `data.network_memberships[]`; optional `data.ttl` |
| Keys | std-base64 raw Ed25519 |
| Unknown participant | any non-200 ⇒ ONIX 401 NACK |
| Revocation | append a `state: revoked` version; wildcard lookup returns live only |
| Not used by ONIX | subscribe/on_subscribe, registry-based routing (uses `context.bpp_uri`), response signing |
| Contract test | `make contract-test` (needs a throwaway `TEST_DATABASE_URL`) |
| Wire-contract detail | [`design.md` Addendum C](design.md) |
| Full E2E task list | this doc, Level 2 (+ server `/opt/dedi-node/HANDOFF.md`) |
