# Discovery: who serves a domain

Answering "who serves domain X" rather than only "what key does this known
subscriber use". The console's *Who serves a domain* tab, and the `?domain=`
filter behind it.

Status: implemented. This was
[issue #14](https://github.com/theflywheel/DeDi-node/issues/14).

## Trust plane, then discovery plane

Until this landed, DeDi could answer one question: given a `subscriber_id` you
already have, what key does it sign with. That is a **trust** question, and it
is what signature validation needs.

It could not answer a **discovery** question. `FindBecknSubscriber` takes an id
you already know and returns one row (`LIMIT 1`); it exists for validation. The
query endpoint filtered on name, state and time, and never reached into the
payload — even though every participant record already carried
`payload->>'domain'`.

So the `url` we published in every record was read by nothing. It was in the
log, and it was decorative.

## A filter, not a new endpoint

The obvious design is `GET /dedi/domain/{domain}`. We did not do that, and the
reason is in `design.md` §120 rather than in this implementation: attribute
filtering over payload fields — role, domain, city/coverage, status — is
specified there as "a namespaced extension backed by the JSONB GIN index",
required for gateway discovery. A separate endpoint would have invented a second
shape for a question the specification already places on the query path.

```
GET /dedi/query/{namespace}/{registry}?domain=retail
```

It is strictly additive. Without the parameter the endpoint behaves exactly as
before and still never reads the payload; only the presence of `?domain=` opts a
caller into the extension. A spec-conformant client cannot be surprised by it.

## What comes back

A deliberately narrow projection — not the record:

```json
{
  "subscriber_id": "bpp.example.com",
  "record_id": "beckn-testnet/subscribers.beckn.one/76EU7ofw…",
  "url": "https://bpp.example.com/beckn",
  "type": "BPP",
  "digest": "632243e7…",
  "state": "live",
  "lookup_url": "/dedi/lookup/beckn-testnet/subscribers.beckn.one/76EU7ofw…"
}
```

Who this is, where to reach them, and what to check them with. The full record
is deliberately absent, and `lookup_url` is deliberately present: **this list is
a starting point, not an answer to be trusted.** This node assembled it, and
only the record and its inclusion proof say what was actually published. A
response that inlined everything would invite a caller to act on an unverified
list, which is the habit the whole project exists to break.

## Two constraints that are not incidental

**It is confined to the eligible namespaces.** A discovery caller has no
`subscriber_id` it already believed in — that is the whole point — so without
the `DEDI_WILDCARD_NAMESPACES` allowlist, anyone able to publish on this node
could insert themselves as a destination for any domain. Trust lookups are
anchored by the id you brought with you; discovery has no such anchor and needs
the allowlist instead.

**The registry in the path scopes the answer.** The store searches every
eligible namespace, which is what discovery means, but a caller asked about one
registry and should not be handed another's participants.

## Both shapes of `domain` resolve

A participant may declare one domain as a string or several as an array, and the
spec permits both. Migration `0008_payload_domain_index.sql` adds two indexes
for that reason:

```sql
CREATE INDEX idx_le_payload_domain ON log_entries ((payload ->> 'domain'))
  WHERE entry_type = 'record';
CREATE INDEX idx_le_payload_gin ON log_entries USING GIN (payload jsonb_path_ops)
  WHERE entry_type = 'record';
```

Supporting only one form would have made a seed using the other silently
invisible — present, correct, and returned by nothing.

## Revocation gains its second meaning

This is the part worth caring about. Before discovery, revoking a participant
meant they stopped being **verifiable**. Now it also means they stop being
**returned as a destination**.

Those are different guarantees, and the second is the one an operator thinks
they are getting when they revoke someone. A test worth running after any
revocation:

```sh
curl -s "$DEDI/dedi/query/$NS/$REG?domain=$DOMAIN" | jq '.data.participants[].subscriber_id'
```

## What this does not do

**It does not make ONIX route through it.** ONIX's router reads the destination
straight out of the message it was handed:

```go
// pkg/plugin/implementation/router/router.go:258
bppURI := getContextString(uriBody.Context, "bpp_uri", "bppUri", "receiverUri")
```

and the `dediregistry` plugin implements only `RegistryLookup`, hard-requiring
both `subscriber_id` and `key_id`. There is no discovery method for it to call.
DeDi can now answer the question; nothing in a stock Beckn deployment asks it.

Closing that needs a Beckn **gateway** plus upstream plugin changes. If the
broadcast story needs demonstrating before then, a small standalone gateway that
queries dedid and fans out is the cheap route.

## A caveat about what is in the registry

Discovery returns whatever was published, which makes bad records visible in a
way trust lookups do not. On the demo network it immediately surfaced two:
participants registered with `railway.internal` URLs that no external router can
resolve, and one subscriber present twice under two record names, returned twice
and revocable only halfway.

Neither was a bug in this surface. Both were seeding bugs that had been
invisible for as long as the only question anyone could ask was "what key does
this known id use".
