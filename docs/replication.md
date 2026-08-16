# Replication and high availability

**Status:** implemented (2026-08-06). Extends `design.md` §4.3 "Scaling: read
replicas + cache" from an aspiration to a mechanism, and revises the v1
non-goal "Federation/gossip between nodes beyond witnessing" only in the narrow
sense described below.

---

## What this is not

Raft is a **crash-fault** protocol. A cluster keeps the directory available
when a replica dies. It does **not** make the directory trustworthy: three
replicas run by one operator follow that operator's leader and will agree on
whatever it says, including a rewritten history.

Tamper evidence comes from exactly where it came from before — the witness
ring, and relying parties checking proofs offline. Nothing in this document
changes or strengthens that, and no part of the UI, the API or the status page
may present cluster agreement as if it were verification. That confusion is the
one genuinely dangerous failure mode here: "three replicas agree" reads like
"three parties verified" to anyone not looking closely, and it is not.

So the two live in separate objects on the wire (`cluster` vs `witnessing` in
`/dedi/network`), and are drawn separately in the UI.

| | Witness ring | Raft cluster |
|---|---|---|
| Members are | independent operators | replicas of one node |
| Answers | "has this history been rewritten?" | "is the directory still up?" |
| Failure it survives | a lying operator | a dead machine |
| Proof? | yes — checkpoints and consistency proofs | none, and none claimed |

---

## Shape

Reads and writes replicate differently because the log's structure treats them
differently.

**Writes are single-writer, always.** The tree needs a total order over leaves
— that is what makes a root mean anything. The Raft log *is* that order, so it
comes from the component whose entire job is producing one.

**Reads are served by every replica.** A follower answers lookups, checkpoints
and proofs from its own database. That is the availability the cluster exists
to provide, and a follower that redirected reads would be giving it away.

```
        writes ──► leader ──► Raft log ──► committed
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
           replica n1          replica n2          replica n3
           (Postgres)          (Postgres)          (Postgres)
              │                   │                   │
              └──────── reads served by any ──────────┘
```

Each replica **materialises** the command stream into its own database and
**computes the tree itself**. The tree is derived, never copied — so a
divergence is a bug that shows up as a differing root rather than silently
propagating.

---

## The invariants

Three things carry the safety of this design. Each has a test.

### 1. Apply is deterministic

Every replica must derive the same leaf bytes from the same command.
`created_at` is inside the Merkle leaf preimage, so it travels **with** the
command rather than being read from each replica's clock — three clocks would
mean three roots for one entry. `store.Apply` refuses an unstamped command
rather than defaulting one, because a default here is a silent fork.

→ `internal/store/determinism_test.go`

### 2. Failures are classified before they are swallowed

- A **deterministic rejection** (bad input, failed precondition, missing
  parent) is a state transition. Every replica reaches it identically, so it is
  returned to the client as a 400 or 409 and the cluster stays in step.
- An **infrastructure failure** (database unreachable, disk full) is local. If
  it were swallowed, this replica would skip an entry every other replica
  applied and serve a different tree from then on, while looking healthy. So
  the replica **halts**. Dying is visible; diverging is not.

Conflating these is the classic way to corrupt a replicated log.

→ `internal/cluster/fsm.go`, `TestDeterministicRejectionsReachTheClientAndDoNotHaltReplicas`

### 3. The stream is applied exactly once, across restarts

Raft holds its applied index **in memory** and replays every committed entry on
restart, because it assumes the state machine was rebuilt from a snapshot and
is empty. That assumption is false here: the state is in Postgres and outlives
the process. A replay would append every command again — and a duplicate append
is a *new version*, not an error, so it would succeed.

The applied index is therefore persisted, **in the same transaction as the
state change**. Snapshots carry it too.

This one is worth dwelling on because it hides so well. Publisher writes carry
preconditions, so a replayed publisher write is rejected by `If-None-Match` and
everything looks correct. Witness verdicts carry no precondition and would have
diverged. The first hand-run restart test passed for the wrong reason.

→ `internal/cluster/replay_test.go`, `internal/store/applied.go`

---

## Checkpoint signing

Signing goes **through** the log rather than being done independently by
whichever process felt like it.

Only the leader signs, over its own committed state. It then replicates the
**signed note**, not an instruction to sign — so every replica stores the
identical signed bytes, and a follower never needs the identity key to serve a
checkpoint. Whichever replica answers, the checkpoint verifies against the one
public key clients have pinned.

`store.SaveCheckpoint` refuses a second, *different* root at a size already
signed (`ErrCheckpointFork`), rather than the previous `ON CONFLICT DO
NOTHING`, which discarded the conflict silently. Two signed roots at one size
is a fork under the node's own key — indistinguishable from the operator
tampering the witness ring exists to catch, and unrecoverable once published.

Under Raft this should be unreachable: committed entries are never lost, so no
two leaders can compute different roots at the same size. It is enforced anyway,
and reaching it halts the replica, because "should be unreachable" is not a
thing to bet a signing key on.

---

## Operating it

Set `DEDI_CLUSTER_ID` to turn replication on. Leaving it unset keeps the node
exactly as it was — an unreplicated node is unchanged by any of this.

| Variable | Meaning |
|---|---|
| `DEDI_CLUSTER_ID` | this replica's stable ID; must appear in the peer list |
| `DEDI_CLUSTER_PEERS` | `id=raft-host:port=https://public-url`, comma separated |
| `DEDI_CLUSTER_BIND` | listen address, defaults to this peer's advertised one |
| `DEDI_CLUSTER_DATA_DIR` | durable dir for the Raft log and snapshots (default `/data/raft`) |
| `DEDI_CLUSTER_BOOTSTRAP` | `true` on exactly one replica, on first start only |

The public URL is what a follower redirects writes to, so omitting it leaves a
follower able to name the leader but not to send a client there.

### Every replica needs its own database

Two replicas sharing one database would each apply every command to the same
rows and corrupt it immediately. Separate logical databases on one Postgres
instance is the supported layout:

```
DEDI_DB_URL=postgres://…/dedi_r1   # replica 1
DEDI_DB_URL=postgres://…/dedi_r2   # replica 2
DEDI_DB_URL=postgres://…/dedi_r3   # replica 3
```

**What that layout does and does not survive.** Separate logical databases on
one instance protect against a dedid process or machine failing — which is most
of what actually goes wrong. They do **not** protect against the Postgres
instance failing: a quorum of replicas would lose their view at once. State the
HA claim as node-level and no more. Moving replicas onto separate instances is
the fix, and nothing in the design changes if you do.

### Identity

All replicas share **one** identity key and one origin. From outside, a cluster
is one node; a checkpoint must verify against one key whichever replica served
it. Do not give replicas separate keys.

### Cluster size

Three tolerates one failure; five tolerates two. Even sizes tolerate no more
failures than the odd size below them while adding ways to lose quorum — the
daemon logs a note if you configure one.

### Writes arriving at a follower

A follower answers `307 Temporary Redirect` to the leader's public URL. 307
rather than 308 or 302 because the method and body must survive — the body is
what the publisher signed — and because leadership moves, so the redirect must
not be cacheable as permanent. During an election there is briefly no leader,
and a follower answers `503` with `Retry-After` and code `NO_LEADER`; that is a
second or two, and it is not a node fault.

### Latency

A write costs a round trip to the second-fastest replica. Keep a cluster
regional. Geographic reach is a different problem and wants read mirrors, not a
stretched quorum.

---

## Observability

`/dedi/network` reports a `cluster` object: role, leader, members, commit and
applied index, and `lag_entries`. Lag is published rather than folded into a
green tick, because **a replica that is up but permanently behind is exactly
what a liveness check cannot see**.

**`lag_entries` alone does not detect that**, and it is worth being precise
about why. It is commit minus applied *on that replica*: the window between
learning of a commit and applying it, which is microseconds. A replica that has
not yet received entries has commit == applied and truthfully reports zero. On
the live cluster one replica sat at applied index 241 while the leader was at
281, and both reported `lag_entries: 0`.

So a reader wanting "how far behind the leader is this replica" must subtract:
leader applied index minus this replica's. The UI does exactly that, because a
browser has asked every replica and can compare them; each half is still that
replica's own claim about itself.

**Closed as of task #30**, in the two halves the gap actually had.

*Scrape every replica, not one URL.* `GET /metrics` serves the same numbers in
Prometheus text exposition format, labelled by `node_id`. A monitor scraping all
three replicas can then do the subtraction the UI does, as an alert expression
rather than as a page someone has to have open:

```promql
max(dedi_cluster_applied_index) - dedi_cluster_applied_index > 100
```

That is the cross-replica comparison; it needs several scrape targets, which is
the normal shape of a monitor, not several endpoints on one node.

*And a signal that works from one URL.* `dedi_cluster_last_contact_seconds` is
how long since the leader last reached this follower. It is the measure
`lag_entries` cannot give: a partitioned replica freezes its commit index
alongside its applied one, so it reports zero lag while its view goes
arbitrarily stale — the failure above, where a replica sat 40 entries behind and
called itself caught up. Time since last contact keeps climbing regardless of
what the replica believes about its own progress. It is `0` on the leader, which
is in contact with itself, and `-1` on a replica that has never heard from a
leader at all, so "never" cannot be misread as "just now".

`/healthz` carries `role`, `has_leader`, `lag_entries` and
`last_contact_seconds` too, for probes that already scrape it. Neither endpoint
*fails* on lag: a follower that is behind still answers reads correctly, just
from an older view, and failing its health check would pull a working replica out
of the load balancer and push its traffic onto the replicas already struggling.
Reporting is the node's job; deciding what is too far behind is the alert rule's.

An unreplicated node reports
`{"enabled": false, "size": 1}` explicitly — absent would be
indistinguishable from a node too old to report it.

A follower's witness reports `standby` rather than going quiet. Followers do not
witness: three replicas independently polling the same target would triple the
load on it to learn one fact, and the verdict is replicated to them anyway.
Without saying so, a healthy follower would look identical to a stalled witness
and two of every three replicas would alarm.

---

## Deferred

- **Read mirrors** — a verifying replica of a *different operator's* log,
  serving that operator's signed checkpoints. Availability across trust
  boundaries, where Raft only works inside one. Needs a bulk entry-range
  endpoint, which does not exist yet.
- **Threshold signing (FROST)** — *t*-of-*n* replicas required to produce a
  checkpoint signature, so no single machine can ever sign a root. This is the
  one option here that would strengthen the *trust* story rather than only
  availability. Real cryptographic engineering; worth a spike.
- **Dynamic membership** — adding and removing replicas is a Raft configuration
  change; today the peer set is static config.
