# Push notification

Revocation used to travel only as fast as caches expired. Measured end to end
against two live beckn-onix v1.8.0 adapters, with the participant record
declaring `ttl: 20`:

```
t+00s  search accepted (the adapter still holds the cached key)
t+05s  search accepted
t+10s  search accepted
t+15s  search REJECTED -> HTTP 401
```

Fifteen seconds during which a compromised key kept validating real traffic. A
TTL *bounds* that window. It cannot remove it, and lowering it trades the
exposure for lookup load on every validator, permanently, against an event that
happens rarely.

A subscriber registered on this node is told the moment a record changes.

## What a push is, and what it is not

The delivery says **which entry changed and where to read it**. It does not
carry the payload.

This is the load-bearing decision in the whole subsystem. A consumer already has
a verifiable way to learn what this node published: read the log, check the
inclusion proof against a signed checkpoint. If a POST could stand in for that,
push would have introduced a way to lie about the directory that the directory's
design otherwise excludes — and it would be the easy path, so it would become
the only path.

Signing does not change this. The signature says the push really came from this
node. Only the log says what the node published.

The sequence number is what makes the distinction practical rather than
preachy: with it the consumer can fetch exactly the entry named, check its
inclusion, and act. The push **accelerates the consumer's own verification**
instead of asking to replace it.

## Measured

Same node, real HTTP consumer, five samples each.

| | latency |
|---|---|
| TTL expiry (`ttl: 20`, two ONIX adapters) | 15 s |
| Push, 5 s poll interval | 0.6 – 4.7 s |
| Push, waking on the write | **32 – 50 ms** |

The middle row is the one worth dwelling on. A polling loop makes the window
*smaller* — a mean of half the sweep interval — but leaves it the same kind of
answer as a TTL: a bound, not a closure. Only waking the loop on the write
actually removes the wait. The ticker stays as a backstop for anything that
reaches the log without passing through the writer, most of all a follower's
applied entries after a promotion.

## How it is built

**No outbox table.** The event a consumer needs is "log entry seq N exists", and
`log_entries` already records exactly that, transactionally. A per-subscription
cursor over `seq` gives the guarantee an outbox is built for — no delivery can
exist for a version that was not committed — without a second table that has to
be kept in step with the first. A new subscription starts at the current head,
so registering one does not replay the whole log at someone who only asked what
happens next.

**Subscriptions are replicated.** This is the part that is easy to get wrong.
Every other operator setting is read from the environment and is identical on
every replica because the operator deployed it that way. A subscription is
created at runtime, by an API call, against whichever replica happens to be
leader — and the three replicas hold three separate logical databases. Written
straight to the leader's database it exists on exactly one node, and the day
that node loses an election the promoted replica has never heard of it:
revocations stop being pushed, nothing errors, and the subscriber's next clue is
a stale key it kept trusting.

So subscriptions go through the same Raft command stream as the log, as a third
command kind next to `append` and `sign_checkpoint`, and they travel in the
snapshot — a replica restored without them would come back having forgotten who
asked to be notified.

The delivery cursor is replicated too, so a failover resumes where the old
leader stopped rather than replaying history at the consumer or skipping the
gap. Only the transient retry state stays in memory, on the same reasoning
`network.Monitor` uses for its observations: it is a fact about the world right
now, not a fact about the directory.

**Delivery is leader-only.** Every replica holds the same subscriptions; if
every replica delivered, a consumer would be told of each revocation three times
and would have no way to tell that from three revocations.

**No per-subscription secret.** Deliveries are signed with the node's identity
key — the same one that signs checkpoints — using the publisher preimage. A
consumer that can already verify this node's log needs no second credential, and
there is no second signing scheme to get subtly wrong. A shared secret would
prove less, to fewer people, and would have to be replicated in the clear
through the Raft log.

**Dead letters.** After the configured attempts a seq is dead-lettered and the
cursor moves past it. Holding the cursor there would queue the next revocation
behind a payload nobody is going to fix — the exact failure this exists to
prevent. What was never delivered is recorded and surfaced, because a
dead-lettered revocation means a consumer still trusting a key this node
withdrew.

## Subscribing

A subscription is per registry and needs a publisher key for the namespace. In
the console: the **Push** tab, a registry and a target URL. Each subscription
is a row there showing its queue and dead letters, with an *unsubscribe* link.
From a shell (the signed-request pattern is in the [quickstart](quickstart.md)):

```sh
echo '{"target_url":"https://consumer.example/dedi-hook"}' > sub.json
dedid sign -key publisher.key -kid op-1 -method POST \
  -path /admin/namespaces/$NS/registries/$REG/subscriptions -body sub.json > headers.txt
curl -s -u "admin:$DEDI_ADMIN_PASSWORD" -X POST \
  "$DEDI/admin/namespaces/$NS/registries/$REG/subscriptions" \
  -H @headers.txt -H 'Content-Type: application/json' --data-binary @sub.json
```

The registry must exist, the URL must be `http` or `https`, and it must
resolve to a public address (below). `GET /admin/namespaces/{ns}/subscriptions`
lists them; `DELETE /admin/namespaces/{ns}/subscriptions/{id}` retires one.
These take no precondition. Set `DEDI_PUBLIC_URL`, or each push's `lookup_url`
is empty.

## What arrives

One `POST` per changed record, in log order, starting from the entry after the
subscription was created:

```json
{
  "origin": "dedi.example.org/log",
  "seq": 1742,
  "namespace": "beckn-testnet",
  "registry": "subscribers.beckn.one",
  "record_name": "76EU7ofw…",
  "version_num": 3,
  "state": "revoked",
  "digest": "632243e7…",
  "created_at": "2026-08-14T10:02:11Z",
  "lookup_url": "https://dedi.example.org/dedi/lookup/beckn-testnet/subscribers.beckn.one/76EU7ofw…"
}
```

Answer any `2xx` to acknowledge. Anything else, or no answer, is retried with
backoff (2 s doubling to a minute); after 6 failed attempts that entry is
dead-lettered and delivery moves on.

## Verifying a push

The request carries the same three headers a publisher's write does, signed by
the node's identity key over the same preimage:

| Header | Value |
|---|---|
| `DeDi-Key-Id` | the node's origin, e.g. `dedi.example.org/log` |
| `DeDi-Timestamp` | RFC 3339, UTC |
| `DeDi-Signature` | base64 Ed25519 signature |

The signed bytes are these seven lines joined by `\n`:

```
dedi/v1/publish
POST
<the request URI the push arrived at, path and query, e.g. /dedi-hook>
<base64 of SHA-256 of the raw body>
<empty: no If-Match>
<empty: no If-None-Match>
<the DeDi-Timestamp value>
```

The public key is the one in the node's verifier key
(`<name>+<hash>+<base64>`): base64-decode the last field and drop its first
byte, an algorithm tag; the remaining 32 bytes are the Ed25519 key. Pin it
once, out of band, as you would for checking checkpoints. Reject a timestamp
far from your clock.

Then do the part that matters. The signature proves the push came from the
node; it does not prove what the node published. Fetch
`lookup_url?version_id=<seq>&proof=inclusion`, check the proof against a
checkpoint signed by the same key, and check its `leaf_index` is `seq` and its
digest is `digest`. Act on that, not on the push.

## The target URL is a request forgery surface

Every other URL in this system is one a caller fetches. This one **the node**
fetches, from inside whatever network the operator deployed it in, on a
schedule, with retries.

Non-public targets are refused. Loopback, private and unspecified ranges for the
obvious reason; link-local because on every major cloud platform
`169.254.169.254` serves instance credentials to whatever asks, and a webhook is
precisely a "make the server fetch this for me" capability.

The check runs twice — when the subscription is created and again at every
delivery — and neither is redundant. Checking only at creation means a name that
resolves publicly today can resolve to the metadata address tomorrow, which is
the target domain operator's decision and not this node's. Checking only at
delivery means a bad target is accepted, stored, and fails silently later, which
the operator discovers by noticing an outage nobody reported.

`DEDI_WEBHOOK_ALLOW_PRIVATE=1` turns the gate off (not
`DEDI_ALLOW_PRIVATE_WEBHOOK_TARGETS`, which despite its name governs the
crawler). A single-VPC deployment whose
consumer is a sibling service legitimately needs it, which is why it is a switch
rather than a prohibition.

## Reading the console

The Push tab shows two different things and keeps them apart deliberately.

Each subscription's counters describe **its queue**: how far behind it is, and
what it was never told. Those counters only move when something is published, so
a delivery loop that died hours ago leaves rows reading "up to date" —
indistinguishable from a healthy queue.

The delivery block describes **the loop**. It is the only thing that can say
whether anything is being sent at all. A node with no loop reports *not running*
rather than defaulting to healthy, and a follower is described as not
delivering rather than as broken.

Same distinction the witness panel makes, for the same reason, in a new place.

## Still open

**ONIX cannot consume a push.** The `dediregistry` plugin implements
`RegistryLookup` and nothing else — there is no hook for an inbound
notification and no cache to invalidate from outside. So the 15 s figure above
is still what a stock adapter experiences. Closing that needs an upstream change
or a shim in front of the adapter; the node side is done and measured, and the
consumer side is not something this repo can land alone.

**No reference consumer verifies inclusion on receipt.** The push carries a seq
precisely so a consumer can, as described above, but the consumers in this
repo's tests only check the signature. A reference consumer that fetches the
entry and checks its proof would make the "hint, not a fact" contract something
you can run rather than something the docs assert.

