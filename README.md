# DeDi Node

Self-hostable, open-source implementation of the [DeDi protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol) — a tamper-evident public directory node backed by a Merkle transparency log instead of a blockchain. Implements all 8 read endpoints of the standard and passes the published [conformance suite](https://github.com/theflywheel/dedi-conformance); serves Beckn ONIX registry lookups (with a patched adapter, see below).

Start with [docs/overview.md](docs/overview.md). Why this exists: [docs/why.md](docs/why.md). How it works: [docs/architecture.md](docs/architecture.md). The original plan, superseded in places: [docs/design.md](docs/design.md).

Every node serves its own documentation at `/docs`, rendered from the copy
embedded in the binary it is running — so a deployment can explain itself
without reaching the internet, and the docs are always the same age as the code.

## Quickstart

From the published image, no checkout needed — [docs/quickstart.md](docs/quickstart.md)
walks through it end to end, including a first signed write:

    docker network create dedi-net
    docker run -d --name dedi-pg --network dedi-net \
      -e POSTGRES_USER=dedi -e POSTGRES_PASSWORD=dedi -e POSTGRES_DB=dedi postgres:16-alpine
    until docker exec dedi-pg pg_isready -h 127.0.0.1 -U dedi -q; do sleep 1; done
    docker run -d --name dedi-node --network dedi-net -p 8080:8080 \
      -e DATABASE_URL='postgres://dedi:dedi@dedi-pg:5432/dedi?sslmode=disable' \
      -e DEDI_ORIGIN=localhost/log flywheelai/dedi-node:latest
    until curl -sf localhost:8080/healthz >/dev/null; do sleep 1; done
    curl -s localhost:8080/dedi/log/checkpoint

Every environment variable and CLI flag: [docs/configuration.md](docs/configuration.md).

From source:

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

**Beckn against this node** — the live two-adapter network and what ONIX needed changing: [docs/beckn-demo.md](docs/beckn-demo.md). Other use cases: [CREST](docs/crest.md).

## Live demo

Three independently operated nodes form a witness ring, and node A carries the `beckn-testnet` registry. The ring is arranged
— A watches B, B watches C, C watches A — so every node is watched by another and none is
privileged. Each node has its own identity key, its own Postgres and its own log; they are separate
operators, not replicas. Every node's explorer shows the whole network and which peers are up.

- **Node A:** https://dedi.beckn.try-dough.com/ (also https://dedid-production-c2cd.up.railway.app/) — the node carrying the Beckn subscribers, plus [/docs](https://dedi.beckn.try-dough.com/docs).
- **Node B:** https://dedi-b-production-cd9f.up.railway.app/
- **Node C:** https://dedi-c-production-c17f.up.railway.app/ — stood up from scratch with `scripts/deploy_railway.py`, key and all.

Each node reports its own state at `/healthz` — database reachability, tree size, and
the age of its newest checkpoint, which is the number that tells you whether a node is
merely up or actually signing.

The ring is the decentralised-trust property: a node cannot rewrite its history without the node
watching it holding a consistency proof that says so. `GET /dedi/witness` on any node publishes
what it has verified about the others, each verdict carrying the target's URL and verifier key so
you can redo the check without trusting the witness. Set `DEDI_WITNESS_TARGET_URL` +
`DEDI_WITNESS_TARGET_KEY` to make any node witness another, and `DEDI_PEERS` to have it display
the network it belongs to.

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
generated beforehand. It prints the verifier key, the public half you hand to someone verifying
you, on every boot, and shows it on its `/` page. (`dedid pubkey` derives it from a key you hold
in a file or `DEDI_KEY`; it cannot read a key kept in the database.)
