# DeDi Node

Self-hostable, open-source implementation of the [DeDi protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol) — a tamper-evident public directory node backed by a Merkle transparency log instead of a blockchain. Beckn One registry compatible (target).

Design: [docs/design.md](docs/design.md). Status: M1 (core node) in progress.

## Quickstart (dev)

    make up          # Postgres 16 on :5433
    make test
