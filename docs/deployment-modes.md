# Deployment modes

dedid ships as one binary with three trust planes. Two of them are **feature
flags**: set the env vars and the plane turns on, unset them and it does not
exist. There are no separate builds, images, or migrations between modes; every
mode is the previous one plus flags, so you can start standalone and upgrade in
place.

| Mode | What runs | Flags | Defends against |
|---|---|---|---|
| **1 · Standalone** | dedid + Postgres | (none) | Silent history rewrites: every read can carry an inclusion proof against a signed checkpoint; clients verify in-browser. Detection requires a client that compares checkpoints over time. |
| **2 · Witnessed** | + any other dedid node watching this one | `DEDI_WITNESS_*` on the watcher | Split views and forked history: an independent operator continuously demands consistency proofs and records verdicts in its own log. Cheating becomes *provable by a third party*. |
| **3 · Anchored** | + a ledger the checkpoints are published to | `DEDI_ANCHOR_*` | Backdating and checkpoint suppression: roots are pinned into an external, ordered timeline the operator does not control. |

Modes compose: a production node typically runs 1+2, adds 3 when an external
timeline is wanted. Nothing about a mode is load-bearing for reads — if a
witness or anchor target is down, the node serves traffic unaffected and the
plane retries.

## Mode 1 — Standalone

```sh
make keygen && docker compose up -d
```

One binary, one Postgres. The transparency log, signed C2SP checkpoints, and
in-browser verification are always on; they are the product, not a mode.

## Mode 2 — Witnessed

Any dedid node can witness any other — the flag goes on the **watcher**:

```sh
DEDI_WITNESS_TARGET_URL=https://dedi.example.org   # node to watch
DEDI_WITNESS_TARGET_KEY=<its verifier key>
DEDI_WITNESS_INTERVAL=60s
```

The overlay `docker-compose.witness.yml` packages this flag-set as a second
node beside the primary (own key, own Postgres):

```sh
docker compose -f docker-compose.yml -f docker-compose.witness.yml up -d
```

Verdicts are browsable under the watcher's `_witness` namespace. Two operators
witnessing each other is the honest minimum for decentralised trust.

**Rings.** A node witnesses exactly one target, so three or more nodes are
arranged as a cycle — A → B → C → A. Every node is then watched by exactly one
other and watches exactly one other: no node is privileged, and none goes
unobserved. A star (everyone watches A) leaves A's watchers unwatched and A
watching nobody, which is strictly weaker for the same number of nodes.

Nodes in a ring do **not** replicate each other. Each keeps its own key, its own
database and its own log; federation beyond witnessing is an explicit non-goal
(design.md §3). What the ring distributes is *trust*, not data.

**Seeing the network.** `DEDI_PEERS` lists the other nodes, as
`name=url` pairs, and the node then polls each for its signed checkpoint and
reports the result on `/dedi/network` and in its explorer:

```sh
DEDI_PEERS='node-b=https://b.example.org,node-c=https://c.example.org'
DEDI_PEER_INTERVAL=30s
DEDI_NODE_NAME=node-a
```

This is an observation, not a proof — it establishes that a peer answered, and
nothing about whether that peer is honest. Only witnessing does that, and the
two are reported separately so one is never mistaken for the other.

## Mode 3 — Anchored

Adapter-based: `DEDI_ANCHOR_BACKEND` selects a `anchor.Ledger` implementation;
new backends are a new adapter file, not a new deployment story.

```sh
DEDI_ANCHOR_BACKEND=cord                # adapter (currently: cord)
DEDI_ANCHOR_RPC_URL=ws://cord:9944      # target chain RPC
DEDI_ANCHOR_SURI=//Alice                # signer (funded account on real chains)
DEDI_ANCHOR_INTERVAL=5m
```

Anchor receipts (tree size → tx/block) land in the `anchors` table, not in the
log itself. The CORD adapter detects the target runtime's signed-extension
layout from on-chain metadata, so the same flags work against:

- **a co-deployed CORD node** — CORD is lightweight (one container); the
  overlay `docker-compose.anchor.yml` runs one next to dedid with persistent
  state:

  ```sh
  docker compose -f docker-compose.yml -f docker-compose.anchor.yml up -d
  ```

  Note the trust honesty: a self-hosted single-validator chain adds
  *persistence and ordering*, not third-party trust — for that, use mode 2, or
  point the same flags at a chain you don't control:

- **an external CORD network** (e.g. Dhiway's) — change `DEDI_ANCHOR_RPC_URL`
  to the network endpoint and `DEDI_ANCHOR_SURI` to a funded account. No other
  change; the adapter refuses loudly if the runtime's extension set is one it
  does not understand.

## Choosing

- Internal registry, single org: **1**.
- Public registry, multiple parties rely on it: **1+2** (find one peer; witness
  each other).
- Institutional / cross-network deployments that want an operator-independent
  timeline: **1+2+3**.
