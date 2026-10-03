# Deployment modes

dedid ships as one binary with three trust planes. Two of them are **feature
flags**: set the env vars and the plane turns on, unset them and it does not
exist. There are no separate builds, images, or migrations between modes; every
mode is the previous one plus flags, so you can start unwitnessed and upgrade in
place.

| Mode | What runs | Flags | Defends against |
|---|---|---|---|
| **1 · Unwitnessed** | dedid + Postgres | (none) | Silent history rewrites: every read can carry an inclusion proof against a signed checkpoint; clients verify in-browser. Detection requires a client that compares checkpoints over time. |
| **2 · Witnessed** | + any other dedid node watching this one | `DEDI_WITNESS_*` on the watcher | Split views and forked history: an independent operator continuously demands consistency proofs and records verdicts in its own log. Cheating becomes *provable by a third party*. |
| **3 · Anchored** | + a ledger the checkpoints are published to | `DEDI_ANCHOR_*` | Backdating and checkpoint suppression: roots are pinned into an external, ordered timeline the operator does not control. |

Mode 1 was called *Standalone* until the node roles below were named, and the
two meanings collided: a **standalone** node in the role sense — its own key,
its own log, nothing above it — is a perfectly ordinary thing to run
*witnessed*. Mode 1 is not about a node's shape, it is about nothing
independent checking it, so *unwitnessed* is what it always meant.

## Two axes, and they are not the same question

**A mode is how strong the evidence is.** A role is what the node is to the
others. They cross freely, and confusing them is how a deployment ends up
feeling safe without being checkable.

| Role | What it is | Weakest mode it can run | Naturally reaches |
|---|---|---|---|
| **standalone** | a directory of its own | 1 | 1+2 once a peer witnesses it |
| **mirror** | serves reads, never routed for writes | 1 | 1+2 — it can be witnessed like any log |
| **witness** | proves another node's log append-only | 1 | it is what *puts another node in mode 2* |
| **replica** | one member of a set, fixed at bootstrap | whatever the set runs | no change: replicas carry **no** trust claim |
| **child** | holds a namespace another node delegates | 1 | 1+2 — the parent normally witnesses it |

Two rows are worth reading twice.

**A witness does not improve its own mode.** Witnessing is something a node does
*for someone else*: it puts the target in mode 2 and leaves the witness exactly
where it was. A ring is how everyone reaches mode 2 at once — each node
witnesses the next, so every node is watched by one it does not control.

**Replicas change no mode at all.** Availability is a third axis
(`DEDI_CLUSTER_*`, see [replication](/docs/replication)) and carries no trust
claim whatsoever: a quorum of replicas run by one operator agrees with that
operator. Whatever a node defends against, it defends against exactly as well
replicated and unreplicated — no better. Do not present replicas as witnesses.

Modes compose: a production node typically runs 1+2, adds 3 when an external
timeline is wanted. Nothing about a mode is load-bearing for reads — if a
witness or anchor target is down, the node serves traffic unaffected and the
plane retries.

## Mode 1 — Unwitnessed

One binary, one Postgres: the [quickstart](/docs/quickstart) is exactly this.
The transparency log, signed C2SP checkpoints, and in-browser verification are
always on; they are the product, not a mode.

## Mode 2 — Witnessed

Any dedid node can witness any other — the flag goes on the **watcher**:

```sh
DEDI_WITNESS_TARGET_URL=https://dedi.example.org/dedi   # node to watch, including /dedi
DEDI_WITNESS_TARGET_KEY=<its verifier key>
DEDI_WITNESS_TARGET_ORIGIN=dedi.example.org/log         # first line of its checkpoint
DEDI_WITNESS_INTERVAL=60s
```

The source tree's `docker-compose.witness.yml` packages this as a second node
beside the compose primary, with its own key and Postgres. Both nodes there
read their keys from files, so make them first and give the witness the
primary's verifier key:

```sh
mkdir -p keys
docker run --rm -v "$PWD/keys:/keys" flywheelai/dedi-node keygen -out /keys/dedid.key -name dev.dedi.local
docker run --rm -v "$PWD/keys:/keys" flywheelai/dedi-node keygen -out /keys/witness.key -name witness.dedi.local
export DEDI_PRIMARY_VERIFIER_KEY='<the verifier key printed for dedid.key>'
docker compose -f docker-compose.yml -f docker-compose.witness.yml up -d
curl -s localhost:8081/dedi/witness | jq .data
```

The first check runs at start-up and may fail while the primary is still
booting; the next one, a minute later, records the verdict. Verdicts are
published at the watcher's `/dedi/witness` ([witnessing](/docs/witnessing)).
Two operators witnessing each other is the honest minimum for decentralised
trust.

**Rings.** A node witnesses exactly one target, so three or more nodes are
arranged as a cycle — A → B → C → A. Every node is then watched by exactly one
other and watches exactly one other: no node is privileged, and none goes
unobserved. A star (everyone watches A) leaves A's watchers unwatched and A
watching nobody, which is strictly weaker for the same number of nodes.

Nodes in a ring do **not** replicate each other. Each keeps its own key, its own
database and its own log. What the ring distributes is *trust*, not data.
Copying data is a different feature with a different trust claim: Raft
[replication](/docs/replication) inside one operator, or a
[mirror](/docs/crawl-mirror) crawling someone else's published files.

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

Pick the **role** from what the node is for, then the **mode** from how much
the reader needs to be able to check:

- Internal registry, single org: role *standalone*, mode **1**.
- Public registry, multiple parties rely on it: role *standalone*, mode **1+2**
  — find one peer and witness each other. Each of you is then in mode 2 because
  of what the *other* runs.
- Institutional / cross-network deployments that want an operator-independent
  timeline: **1+2+3**.
- A namespace someone else should govern: role *child*, and the parent
  witnesses it, so it arrives in mode 2 rather than being upgraded to it later.
- Reach without authority: role *mirror*. It cannot write, and everything it
  serves is still checkable against the original publisher's signature
  ([crawler and mirror](/docs/crawl-mirror)).

Add replicas when the cost of the directory being *unreachable* matters — for
Beckn, an unresolvable subscriber key is a 401 NACK on every message in flight.
That is an availability question and is orthogonal to the mode you pick.
