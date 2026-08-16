# DeDi Node

Self-hostable, open-source implementation of the [DeDi protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol) — a tamper-evident public directory node backed by a Merkle transparency log instead of a blockchain. Beckn One registry compatible (target).

Why this exists (the story): [docs/why.md](docs/why.md). Design: [docs/design.md](docs/design.md). Status: M1 (core node) in progress.

## Quickstart

    make up                      # Postgres 16 on :5433
    make keygen                  # node identity key -> keys/dedid.key (prints public verifier key)
    DEDI_KEY_FILE=keys/dedid.key go run ./cmd/dedid serve &   # read plane on :8080
    go run ./cmd/dedid seed -file examples/beckn-seed.json

    curl -s "localhost:8080/dedi/lookup/flywheel-net/participants/bap.demo.theflywheel.in?proof=inclusion" | jq .
    curl -s localhost:8080/dedi/log/checkpoint

Or fully containerized: `make keygen && docker compose up --build`.

Trust planes are feature flags on one binary: witnessing (`DEDI_WITNESS_*`) and
ledger anchoring (`DEDI_ANCHOR_*`) turn on by env, packaged as compose overlays
(`docker-compose.witness.yml`, `docker-compose.anchor.yml`). See
[docs/deployment-modes.md](docs/deployment-modes.md).

Onboarding is operator-gated — no self-service registration; every governance
decision is a log entry. See [docs/governance.md](docs/governance.md).

## Development

    make test    # integration tests (needs make up)

## Compatibility

dedid is proven from the outside in — through its own CLI and existing ecosystem clients, never bespoke harnesses:

- `make test` — the node's own integration suite (includes offline inclusion-proof verification).
- `make contract-test` — boots a real `dedid`, seeds it via `dedid seed`, and runs the [beckn-onix `dediregistry` client](https://github.com/beckn-one/beckn-onix) (the ONIX adapter's registry plugin, pinned to v1.8.0) against it over the wire: subscriber key lookup, node lookup, registry metadata, network-membership enforcement, and unknown-participant rejection.

Note: a stock ONIX adapter pins the registry URL to `fabric.nfh.global` via a signed "locked Beckn constant" — pointing a full ONIX deployment at a self-hosted registry currently requires a patched adapter build. See docs/design.md Addendum C.

**Setting up Beckn against this node** — the runbook (contract test + full starter-kit E2E procedure): [docs/beckn-demo.md](docs/beckn-demo.md).

## Live demo

Three independently operated nodes carry the `beckn-testnet` registry, arranged in a witness ring
— A watches B, B watches C, C watches A — so every node is watched by another and none is
privileged. Each node has its own identity key, its own Postgres and its own log; they are separate
operators, not replicas. Every node's explorer shows the whole network and which peers are up.

- **Node A:** https://dedi.beckn.try-dough.com/ — the node carrying the Beckn subscribers, plus [/docs](https://dedi.beckn.try-dough.com/docs) (sequence diagrams + test cases).
- **Node B:** https://dedi-b-production.up.railway.app/
- **Node C:** https://dedi-c-production.up.railway.app/ — stood up from scratch with `scripts/deploy_railway.py`, key and all.
- **Status page:** https://status.beckn.try-dough.com/ — per-node health, log and network monitors, plus the trust ring.

The ring is the decentralised-trust property: a node cannot rewrite its history without the node
watching it holding a consistency proof that says so, recorded in that node's own `_witness`
namespace. Set `DEDI_WITNESS_TARGET_URL` + `DEDI_WITNESS_TARGET_KEY` to make any node witness
another, and `DEDI_PEERS` to have it display the network it belongs to.

### Run a node of your own

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/mfW-Zh)

One click gives you a Postgres with a volume and a node on a public domain,
signing checkpoints, with nothing to fill in — it pulls
[`flywheelai/dedi-node`](https://hub.docker.com/r/flywheelai/dedi-node) and
mints its own identity key on first boot. It serves reads and accepts no writes
until you generate a publisher key, which is the intended starting state.
Details, and how to change what the button deploys:
[docs/railway-template.md](docs/railway-template.md).

From a checkout instead, which is how the nodes above were provisioned:

    scripts/deploy_railway.py --name my-dedi-node

Provisions a Postgres, the node, and a domain, and waits until the node is actually serving. The
node mints its own identity key on first boot and keeps it in its database, so nothing has to be
generated beforehand — `dedid pubkey` recovers the verifier key later if you need to hand it to
someone verifying you.
