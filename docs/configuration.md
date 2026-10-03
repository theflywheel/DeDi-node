# Configuration

Every environment variable `dedid serve` reads, and every CLI subcommand. The
node has no config file: one binary, configured by its environment, so the same
image is every role. Unset means the default in the table; a duration is Go
syntax (`30s`, `5m`, `1h`).

## Core

| Variable | Default | What it does |
|---|---|---|
| `DEDI_DB_URL` | — | Postgres URL. Wins over `DATABASE_URL` when both are set. |
| `DATABASE_URL` | `postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable` | What managed Postgres add-ons inject; used when `DEDI_DB_URL` is unset. Migrations run on start. |
| `DEDI_KEY` | — | The node identity key itself (`PRIVATE+KEY+…`, from `dedid keygen`). Signs checkpoints, DeDi files and webhook pushes. |
| `DEDI_KEY_FILE` | `dedid.key` if it exists | A file holding that key. An explicitly named file that cannot be read stops the node rather than minting a new identity. |
| *(neither)* | | The node generates a key on first start and keeps it in its database, so it keeps its identity across restarts. Refused when `DEDI_CLUSTER_ID` is set: replicas must share one key. |
| `DEDI_ORIGIN` | `dev.dedi.local/log` | The log's name: first line of every checkpoint, and the key name of a self-generated key. Use your domain. |
| `DEDI_LISTEN` | `:8080` | Listen address. |
| `PORT` | — | Used as `:$PORT` when `DEDI_LISTEN` is unset; what PaaS platforms set. |
| `DEDI_CHECKPOINT_INTERVAL` | `30s` | How often to sign a checkpoint. No new one is written while the tree is unchanged. |
| `DEDI_TTL` | `300` | Seconds. The `ttl` in read responses and the `max-age` on latest-version reads. A record payload's own integer `ttl` overrides it for that record. |
| `DEDI_PUBLIC_URL` | — | This node's external base URL. Used as the callback for children, in push `lookup_url`s, and as the publisher domain in DeDi files (falls back to the request's Host). |
| `DEDI_NODE_NAME` | the origin | Label for this node in the network view. |
| `DEDI_VERIFIER_KEY` | derived from the identity key | Overrides the verifier key the node publishes on its pages and hands to children and domain challenges. Rarely needed: the key is always derivable. |
| `DEDI_STATS_FLUSH_INTERVAL` | `10s` | How often the in-memory request counters are folded into the database, so `/dedi/stats` survives restarts and sums across replicas. |

## Write plane

| Variable | Default | What it does |
|---|---|---|
| `DEDI_PUBLISHER_KEYS` | — | `kid:namespace:base64pubkey` entries, comma or whitespace separated, from `dedid pubkeygen`. Unset: no write routes and no `/admin` at all (404). Set: the 13 signed `/admin` routes exist. Each key may write only its own namespace. Append to the list; replacing it revokes every other key. |
| `DEDI_WILDCARD_NAMESPACES` | — | Comma-separated namespaces allowed to answer the Beckn wildcard lookup (`…/subscribers.beckn.one/{key_id}`) and `?domain=` discovery. Set it only if this node serves the Beckn ONIX registry lookup. On a node with no publisher keys, unset means every namespace may answer. |
| `DEDI_ADMIN_USER` | `admin` | HTTP Basic user for `/admin` and every write route. |
| `DEDI_ADMIN_PASSWORD` | — | Turns that Basic-auth gate on. Unset on a node with publisher keys, the node logs a warning on every boot. A read-only node has no write routes for it to guard and logs nothing. The publisher signature is still required either way. |

## Witnessing and the network view

| Variable | Default | What it does |
|---|---|---|
| `DEDI_WITNESS_TARGET_URL` | — | Turns witnessing on. The target's API base **including `/dedi`**, e.g. `https://b.example/dedi`. |
| `DEDI_WITNESS_TARGET_KEY` | — | The target's verifier key. Get it out of band. |
| `DEDI_WITNESS_TARGET_ORIGIN` | `target` | Must equal the first line of the target's checkpoint. |
| `DEDI_WITNESS_INTERVAL` | `60s` | How often to check the target. |
| `DEDI_WITNESS_RECORD_INTERVAL` | `1h` | Write a consistent verdict at most this often per target; an alarm is written at once. `0` writes one for every change, the old behaviour. See [witnessing](witnessing.md). |
| `DEDI_PEERS` | — | Other nodes to show in the network view: comma-separated `url` or `name=url`. Observation only (did it answer), never proof. |
| `DEDI_PEER_INTERVAL` | `30s` | How often to poll peers. |
| `DEDI_EXTERNAL_STATUS_URL` | — | Link to an external monitor watching this node, shown on `/status`. Empty by design: the node will not imply someone independent watches it. |
| `DEDI_DEMO_URL` | `https://schemes.proto.theflywheel.in/` | Target of the "Demo" tab in the page navigation. Only `http(s)` URLs are accepted. |

## Replication (Raft)

See [replication](replication.md). All replicas need the **same** `DEDI_KEY` or
`DEDI_KEY_FILE` and the same `DEDI_ORIGIN`, and each its own database.

| Variable | Default | What it does |
|---|---|---|
| `DEDI_CLUSTER_ID` | — | Turns clustering on: this replica's id, which must appear in the peer list. |
| `DEDI_CLUSTER_PEERS` | — | `id=raft-host:port=https://public-url`, comma or whitespace separated. The URL is where followers redirect writes. |
| `DEDI_CLUSTER_BIND` | this peer's advertised address | Raft listen address. |
| `DEDI_CLUSTER_DATA_DIR` | `/data/raft` | Durable directory for the Raft log and snapshots. |
| `DEDI_CLUSTER_BOOTSTRAP` | — | `true` on exactly one replica, on its first start only. |

## Delegation (child nodes)

See [delegation](delegation.md). The parent needs only `DEDI_PUBLIC_URL`. The
child's values are rendered by the parent's console.

| Variable | Default | What it does |
|---|---|---|
| `DEDI_CHILD_WITNESS_INTERVAL` | `60s` | Parent: how often to witness each enrolled child. |
| `DEDI_PARENT_URL` | — | Child: the parent to enrol with (`POST /enrol`). |
| `DEDI_PARENT_KEY` | — | Child: the parent's verifier key, recorded in the child's delegated namespace. |
| `DEDI_ENROL_NAMESPACE` | — | Child: the namespace being claimed. |
| `DEDI_ENROL_TOKEN` | — | Child: the one-time offer token. Enrolment runs only when this, the namespace, the parent URL and `DEDI_PUBLIC_URL` are all set. |

## Crawling, push and anchoring

| Variable | Default | What it does |
|---|---|---|
| `DEDI_CRAWL_DOMAINS` | — | Turns the crawler on: comma-separated domains whose DeDi files to verify and mirror. See [crawler and mirror](crawl-mirror.md). |
| `DEDI_CRAWL_INTERVAL` | `1h` | How often to crawl them. |
| `DEDI_ALLOW_PRIVATE_WEBHOOK_TARGETS` | — | `true` lets the **crawler** fetch from non-public addresses. Despite its name it does not affect webhooks. |
| `DEDI_WEBHOOK_ALLOW_PRIVATE` | — | `1` lets **webhook subscriptions** target non-public addresses. Off by default because the node makes these requests from inside your network. |
| `DEDI_ANCHOR_BACKEND` | — | Turns anchoring on. Only `cord` exists. |
| `DEDI_ANCHOR_INTERVAL` | `5m` | How often to anchor the latest checkpoint. |
| `DEDI_ANCHOR_RPC_URL` | `ws://127.0.0.1:9944` | The chain's RPC endpoint. |
| `DEDI_ANCHOR_SURI` | — | The signing account (a funded one on a real chain). |
| `DEDI_ANCHOR_SS58` | `29` | SS58 address prefix of the target chain. |

The two private-target switches are separate on purpose and spelled
differently by accident. Set the one for the subsystem you mean.

## Tests

`TEST_DATABASE_URL` points the test suite at a throwaway Postgres; tests that
need it fail rather than skip when it is unset, unless the run is `-short` or
`DEDI_TEST_SKIP_WITHOUT_DB` is set.

## The CLI

`dedid <command> [flags]`. In the Docker image the entrypoint is `dedid`, so
`docker run --rm flywheelai/dedi-node <command> …` runs any of these.

| Command | Flags | What it does |
|---|---|---|
| `serve` | none; configured by the environment above | Runs the node. The image's default command. |
| `keygen` | `-out` (`dedid.key`), `-name` (`dedi.local`) | Writes a node identity key and prints its verifier key. Only needed where the key must be supplied: a cluster, or a key you keep outside the database. |
| `pubkey` | `-key` (default `$DEDI_KEY`, else `dedid.key`) | Prints the verifier key for a node key you hold. A self-generated key lives in the database instead; read its verifier key from the boot log. |
| `pubkeygen` | `-kid`, `-namespace` (both required), `-out` (`publisher.key`) | Mints a publisher key. Writes the private half (refusing to overwrite an existing file) and prints the `DEDI_PUBLISHER_KEYS` entry. |
| `sign` | `-key` (`publisher.key`), `-kid`, `-path` (both required), `-method` (`POST`), `-body` (file), `-create`, `-if-match <tag>`, `-curl` | Prints the headers that authenticate one write: `DeDi-Key-Id`, `DeDi-Timestamp`, `DeDi-Signature`, plus the precondition. At most one of `-create` (the target must not exist) and `-if-match` (replace exactly this version); neither means no precondition, for routes that take none. `-curl` prints them as `-H` flags. A signature is valid for five minutes either side of the node's clock. |
| `seed` | `-file` (required), `-by` (`seed`) | Appends a seed file's namespace, registries and records straight to the database named by `DEDI_DB_URL`/`DATABASE_URL`, bypassing the write plane. For bootstrapping and tests; `-by` is recorded as `created_by`. Do not point it at a cluster replica's database. |

Which writes need which precondition: publishing a namespace, registry or
record needs `-create` or `-if-match` (without one the node answers `428`);
revoking a record needs `-if-match`. Children, node-config, domain
verification and webhook subscriptions take no precondition. See
[API](api.md).
