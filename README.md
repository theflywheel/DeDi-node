# DeDi Node

Self-hostable, open-source implementation of the [DeDi protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol) — a tamper-evident public directory node backed by a Merkle transparency log instead of a blockchain. Beckn One registry compatible (target).

Design: [docs/design.md](docs/design.md). Status: M1 (core node) in progress.

## Quickstart

    make up                      # Postgres 16 on :5433
    make keygen                  # node identity key -> keys/dedid.key (prints public verifier key)
    DEDI_KEY_FILE=keys/dedid.key go run ./cmd/dedid serve &   # read plane on :8080
    go run ./cmd/dedid seed -file examples/beckn-seed.json

    curl -s "localhost:8080/dedi/lookup/flywheel-net/participants/bap.demo.theflywheel.in?proof=inclusion" | jq .
    curl -s localhost:8080/dedi/log/checkpoint

Or fully containerized: `make keygen && docker compose up --build`.

## Development

    make test    # integration tests (needs make up)

## Compatibility

dedid is proven from the outside in — through its own CLI and existing ecosystem clients, never bespoke harnesses:

- `make test` — the node's own integration suite (includes offline inclusion-proof verification).
- `make contract-test` — boots a real `dedid`, seeds it via `dedid seed`, and runs the [beckn-onix `dediregistry` client](https://github.com/beckn/beckn-onix) (the ONIX adapter's registry plugin, pinned to v1.8.0) against it over the wire: subscriber key lookup, node lookup, registry metadata, network-membership enforcement, and unknown-participant rejection.

Note: a stock ONIX adapter pins the registry URL to `fabric.nfh.global` via a signed "locked Beckn constant" — pointing a full ONIX deployment at a self-hosted registry currently requires a patched adapter build. See docs/design.md Addendum C.
