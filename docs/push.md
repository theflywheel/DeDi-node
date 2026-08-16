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

`DEDI_WEBHOOK_ALLOW_PRIVATE=1` turns the gate off. A single-VPC deployment whose
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

**Nothing verifies inclusion on receipt.** The push carries a seq precisely so a
consumer can, but the consumers in this repo's tests only check the signature.
A reference consumer that fetches the entry and checks its proof would make the
"hint, not a fact" contract something you can run rather than something the
docs assert.

---

# Discovery

`GET /dedi/query/{namespace}/{registry}?domain=retail`

DeDi was a **trust** plane: it could answer "what key does this known
`subscriber_id` use" and nothing else a router could act on. `FindBecknSubscriber`
takes a subscriber_id you already have and returns one record; it exists for
signature validation. So the `url` field in every participant record was in the
log and decorative — nothing ever read it.

**A filter, not a new endpoint.** `design.md` §120 already places this: attribute
filtering over payload fields (role, domain, city/coverage, status) is "a
namespaced extension backed by the JSONB GIN index", required for gateway
discovery. A separate endpoint would have invented a second shape for a question
the spec puts here. It is opt-in by the presence of `?domain=`, so `/dedi/query`
without it behaves exactly as before and still never reaches into the payload.

## What this changes about revocation

Revocation gains a second meaning. It already stopped a participant being
**verifiable**; now it also stops them being **returned as a destination**.

Those are different protections. The first stops a forged message being
accepted. The second stops a real one being sent somewhere it should not go —
and it is the one that matters on this surface, which is why the answer carries
the node's short TTL rather than anything longer. A long `max-age` here would
reintroduce exactly the staleness window push exists to close, on the read that
decides where traffic goes.

## The allowlist matters more here

The eligible-namespace constraint (`design.md:256`) applies, and for a sharper
reason than it does for lookup.

A wildcard lookup is at least anchored to a `subscriber_id` the caller already
believed in. A discovery caller has no such anchor — it is asking to be told who
exists. Without the allowlist, anyone able to publish a record on this node
could insert themselves as a destination for any domain and receive traffic
meant for someone else.

## The list is a starting point

The response carries a `lookup_url` per participant, and the console renders it
as a *verify* link on every row rather than a summary the operator reads and
believes. This node assembled the list; only each record and its inclusion proof
say what was actually published. Same contract as a push: a pointer to what can
be checked, not a claim to be trusted.

## Still open

**Making ONIX route through it**, which GH #14 puts explicitly out of scope. The
`dediregistry` plugin implements only `RegistryLookup` and hard-requires both
`subscriber_id` and `key_id`; there is no discovery method to call, and
`router.go:258` reads `bppUri` straight out of the message context, so the
router never consults the registry at all. That needs a Beckn **gateway** plus
upstream plugin changes. If the broadcast story is wanted as a demo, a small
standalone gateway in the demo stack that queries dedid and fans out is the
cheap route.
