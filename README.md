# DeDi Node

Self-hostable, open-source implementation of the [DeDi protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol) — a tamper-evident public directory node backed by a Merkle transparency log instead of a blockchain. Beckn One registry compatible (target).

Design: [docs/design.md](docs/design.md). Status: M1 (core node) in progress.

## Quickstart

    make up                      # Postgres 16 on :5433
    make keygen                  # node identity key -> keys/dedid.key (prints public verifier key)
    DEDI_KEY_FILE=keys/dedid.key go run ./cmd/dedid serve &   # read plane on :8080
    go run ./cmd/dedid seed -file examples/beckn-seed.json

    curl -s localhost:8080/dedi/lookup/flywheel-net/participants/bap.demo.theflywheel.in?proof=inclusion | jq .
    curl -s localhost:8080/dedi/log/checkpoint

Or fully containerized: `make keygen && docker compose up --build`.

## Development

    make test    # integration tests (needs make up)
