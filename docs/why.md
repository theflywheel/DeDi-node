# Why DeDi Node exists

**Author:** Chakshu (FWAI Technologies / Flywheel)
**Date:** July 2026
**Status:** Narrative / rationale. How it works is in [architecture](architecture.md); the
proof it works is in [the Beckn use case](beckn-demo.md). This is the *why* the other two assume.
The original plan is kept as [design](design.md).

---

## The short version

I built this because I was running a real Beckn network in production, and the single most
important component in it — the **registry**, the thing every party trusts to answer "who is
this and is their signature valid" — was the least trustworthy, least observable, and least
ownable piece of the whole stack. The only "real" registry was a hosted, blockchain-anchored
service I couldn't self-host and couldn't audit. So the choice was: trust a black box, or
build the box.

DeDi Node is the box. It's a self-hostable registry that makes its own answers *verifiable* —
every lookup can carry a cryptographic inclusion proof against a signed checkpoint, and an
independent witness node continuously proves the log was never rewritten. It gets there with a
**transparency log**, not a blockchain, which is what makes it a single Go binary you can
`docker run` beside a Postgres instead of a platform you have to join.

## Where this actually started

Not in the abstract. On [OpenAgriNet](https://github.com/theflywheel) / Amul — a production
agricultural Beckn network where a farmer-facing chat pulls schemes, weather, and mandi prices
across network boundaries from another network (Bharat Vistaar) over Beckn. Getting that
cross-network flow live meant standing up and operating the full canonical Beckn discovery
stack myself:

```
consumer → ONIX BAP adapter (signs) → gateway (broadcast) → registry (resolves identity)
         → partner BPP → on_search callback → BAP receiver (validates signature) → consumer
```

Five moving parts — adapter, gateway, registry, a registry-aggregating middleware, and an
async callback leg — all of which have to be correct *at the same time* for one search to
return one result.

## What kept breaking — and the pattern underneath it

Over a long stretch of operating this, it broke in distinct, repeatable ways:

- **Boot-order fragility.** The gateway cached its domains at startup; if it booted before the
  registry/middleware was ready, every search 400'd with a null-domain error until a manual
  restart.
- **The async callback was a silent single point of failure.** Discovery isn't a
  request/response — the result comes back later as a separate signed `on_search` POST. One
  wiped ingress route (a `405` on the callback path) killed the entire return leg with no error
  at the caller; searches just timed out.
- **Federation didn't scale.** Talking to N partner networks meant a registry-aggregating
  middleware fanning across N registries — an O(N²) mesh, plus manual cross-registration of
  every party in every direction.
- **The registry itself was opaque.** Default `root/root` admin credentials, no tamper-evidence,
  no audit trail. Trust was "because the registry says so." There was no way for a relying party
  to *verify* an answer — only to accept it.
- **It needed a babysitter.** The stack was reliable only with a self-healing guard I wrote to
  re-apply config and restart components when they drifted. A guard is a symptom, not a fix.

Every one of these is really the same problem wearing different clothes. **The registry is the
trust root of a Beckn network, and the registry I had gave me no trust** — no verifiability, no
audit, no ownership, and a discovery model whose failure modes were invisible until a demo went
dark. And the one registry that was *meant* to be authoritative — the hosted
`fabric.nfh.global`, anchored on a blockchain — I could neither self-host nor inspect. For a
public-good network that's supposed to be *sovereign*, depending on someone else's unauditable
black box for the definition of "who is on the network" is exactly backwards.

## The realization

Two facts collided:

1. **Beckn made DeDi load-bearing.** The ONIX adapter's `dediregistry` plugin is on the
   signature-validation hot path of every message; VC revocation is checked against DeDi. The
   registry *is* a DeDi registry. But there is **no self-hostable, open-source DeDi node** — only
   the hosted platform. So a sovereign Beckn network is, with a stock stack, impossible.
2. **DeDi's trust properties don't need a blockchain.** What DeDi actually requires is
   tamper-evidence, provenance, version history, and offline verifiability. Every one of those is
   exactly what a **transparency log** delivers — a Merkle tree over an append-only record log,
   signed checkpoints, and external witnesses — at a fraction of the operational weight of a
   chain. The blockchain was accidental complexity, not essential.

Put those together and the gap is obvious: *the first self-hostable, Beckn-One-compatible DeDi
registry, backed by a transparency log instead of a chain.* That's this repo.

## What I built

- **`dedid`** — a single Go binary + Postgres, published as the `flywheelai/dedi-node` image.
  No chain dependency. ([quickstart](quickstart.md))
- **Verifiability as a first-class feature.** Any lookup can return an inclusion proof against a
  signed checkpoint; relying parties verify offline. Consistency proofs between checkpoints make
  history-rewriting *detectable*, not just discouraged.
- **An independent witness node** that continuously re-verifies the primary's log is append-only
  and records each verdict under its own namespace. That is the decentralised-trust property made
  concrete: the primary cannot rewrite history without an independent party catching it. (The
  public node at https://dedi.beckn.try-dough.com publishes its verdicts at `/dedi/witness`.)
- **Governance as an append-only log**, closed by default. Onboarding, rotation, and revocation
  are log entries — provable, ordered, witnessable — not opaque admin mutations. ([governance.md](governance.md))
- **Proven from the outside in.** A contract test drives the *real* ONIX `dediregistry` client
  against a live `dedid`, and a live two-adapter Beckn network resolves every signature against
  `dedid` with `fabric.nfh.global` out of the loop. ([beckn-demo.md](beckn-demo.md))

## Why it matters

| The old way | With DeDi Node |
|---|---|
| Trust because the registry *says so* | Trust because you can *verify the proof* |
| `root/root`, opaque, no audit | Governance is an auditable, witnessed log |
| Hosted black box, can't self-host | Single binary, self-hosted, sovereign |
| 5 moving parts that need a babysitter | One node; witnessing/anchoring are env flags |
| Blockchain-weight for chain-free needs | Transparency-log-weight, chain optional |

## What it does *not* solve (the honest part)

A rationale that only lists wins is marketing. The real boundaries:

- **Maturity.** The node is young: it passes the published conformance suite and runs a live
  network, but it has not had years of production. The stack it critiques, for all its pain,
  is battle-tested; this is newer.
- **The locked registry URL.** A *stock* ONIX adapter pins the registry to `fabric.nfh.global`
  as a signed, embedded constant — pointing a full deployment at a self-hosted registry needs a
  patched adapter build (and the upstream PR that unlocks it). The naive "just change the URL"
  drop-in story is, today, not true unmodified. ([beckn-demo.md](beckn-demo.md), design.md Addendum C)
- **Interop is a boundary, not a solvent.** DeDi replaces *my* trust root. Partner networks that
  run gateway-based registries still speak their model — so adopting DeDi doesn't erase
  cross-network interop; it needs a **bridge at the edge**. The honest framing is *"DeDi as the
  sovereign trust core, an interop shim at the boundary,"* not *"rip everything out."*
- **Discovery-model shift.** DeDi-native discovery is lookup/pull; some Beckn flows assume
  gateway broadcast. The mapping is clean for the use cases proven so far, not automatically for
  all of them.

## The one-line case

The reliability, trust, and ownership rot I kept fighting all lived in one place — the registry
at the center of the network. **DeDi Node fixes the center and pushes interop to the edge**, and
does it with a transparency log light enough that owning your own trust root is a `docker compose
up`, not a platform you have to ask permission to join.
