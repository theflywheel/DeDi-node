# Use case: a Beckn network

Beckn is the first thing this node was built for. Every signed Beckn message is
checked by the receiving ONIX adapter against the sender's key in a DeDi
registry, so the registry is the trust root of the network. This page is the
live deployment that shows it working: two ONIX adapters verifying each other's
signatures against a self-hosted node instead of the hosted `fabric.nfh.global`.

## The live network

The registry is the public node, **https://dedi.beckn.try-dough.com**. The
participants are in namespace `beckn-testnet`, registry
`subscribers.beckn.one`:

```sh
curl -s 'https://dedi.beckn.try-dough.com/dedi/query/beckn-testnet/subscribers.beckn.one' \
  | jq -r '.data.records[] | "\(.state)\t\(.record_name)"'
```

Around it runs a two-adapter Beckn network, described in this repository's
`deploy/beckn`:

```
sandbox-bap ──> onix-bap ──signed──> onix-bpp ──> flywheel-bpp ──> schemes/weather/mandi/news providers
                   │   <──signed on_discover──    │
                   └──── key lookups ──> dedi.beckn.try-dough.com <──┘
```

| Service | What it is |
|---|---|
| `onix-bap`, `onix-bpp` | beckn-onix adapters, from a fork carrying three demo patches (below). They sign outbound messages and verify inbound ones. |
| `sandbox-bap` | a minimal BAP application that triggers a search and collects callbacks. |
| `flywheel-bpp` | the BPP application. It fronts the Flywheel demo's providers (schemes, weather, mandi prices, news) and returns their `on_discover` whole. |
| `redis` | the adapters' cache. |

Every arrow between the adapters is a signed message whose sender key is looked
up here:

```
GET /dedi/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}
```

That is ONIX's wildcard lookup. It resolves because the node runs with
`DEDI_WILDCARD_NAMESPACES=beckn-testnet`: only that namespace may answer for a
`subscriber_id`, so a publisher scoped to some other namespace on the node
cannot impersonate a participant. Try it:

```sh
curl -s 'https://dedi.beckn.try-dough.com/dedi/lookup/bpp.example.com/subscribers.beckn.one/76EU7ofwRCF1aobQkShARrf1PAUsNpHqWUJoynPu9w45YFKmzqaPmy?proof=inclusion' \
  | jq '{details: .data.details, ttl: .data.ttl, checkpoint: .proof.checkpoint}'
```

## How a participant is recorded

- **The record name is the key id** the adapter sends, because the wildcard
  lookup matches on it. The `{subscriber_id}` in the path must equal the
  payload's `subscriber_id` (or the name of the namespace). One subscriber with
  two keys is two records. See [onboarding](onboarding.md).
- **`status: SUBSCRIBED`** or no status at all is required for a lookup to
  return a participant, on the exact path as well as the wildcard search. A
  revoked current version is a `404` on every path. `?include_revoked=true` and
  pinned versions still answer, for the operator and for history. ONIX treats
  any non-`200` as an unknown sender.
- **`network_memberships`** must include the network the adapter is configured
  for (`beckn.one/testnet` here), or the adapter rejects the sender.
- **`ttl: 20`** on these records. ONIX caches keys for the `ttl` the registry
  returns; a short one makes a revocation bite in seconds. Measured: 15 s. See
  [revocation](revocation.md).

The participants are published through the signed write plane like any other
record, and every version is in the log with an inclusion proof.

## What ONIX needed changing

A stock ONIX adapter cannot be pointed at a self-hosted registry by
configuration. The registry URL is a signed, embedded "locked Beckn constant"
fixed to `https://fabric.nfh.global/registry/dedi`, and setting any other URL
fails adapter start-up. So this node is not a drop-in for an unmodified
adapter.

The fork used here adds an environment override for locked constants
(`ONIX_OVERRIDE_<PLUGIN>_<KEY>`, so `ONIX_OVERRIDE_DEDIREGISTRY_URL` points the
adapter at `https://dedi.beckn.try-dough.com/dedi`), a Redis logical-database
index so both adapters share one Redis, and an image with its config baked in.
The registry base URL ends in `/dedi`: the client appends `/lookup/…` itself.
Upstreaming the override is the change that would make self-hosted Beckn
registries possible with a stock adapter.

## The contract test

The source tree also carries an automated check that does not need the live
network: `make contract-test` boots a real `dedid`, seeds it with the starter
kit's testnet identities, and drives the real beckn-onix `dediregistry` client
(v1.8.0) against it over HTTP. Six subtests: key lookup by subscriber and key
id, network membership accepted and rejected, an unknown participant refused,
node lookup by three-part id, and registry metadata. It needs a throwaway
Postgres in `TEST_DATABASE_URL` and is run by hand; CI does not run it.

## What this does not show

- **Discovery routing.** ONIX takes the destination from the message
  (`bpp_uri`); it never asks the registry who serves a domain, so
  [discovery](discovery.md) is answered here and consulted by nothing in a
  stock network.
- **Push.** ONIX has no hook for a pushed revocation, so the adapters still
  wait out the `ttl`. See [push](push.md).
- **Interop with networks on `fabric.nfh.global`.** Participants registered
  there are not in this registry, and the reverse.

## Seeing the trust properties

- The node's checkpoint: `https://dedi.beckn.try-dough.com/dedi/log/checkpoint`.
- What it has verified about another node:
  `https://dedi.beckn.try-dough.com/dedi/witness`.
- Its pages: `/` for an overview, `/network` for who watches whom, `/verify`
  to check a proof in your browser.
