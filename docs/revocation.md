# Revocation

Withdrawing a binding: the console's *Revoke a participant* panel, which opens against the participant selected in the Participants table, what "revoked"
means to everyone downstream, and how long it takes to bite.

Revocation is the operation the whole design is judged on. Admitting someone is
easy and reversible; removing them is neither, and a registry that cannot
credibly remove an identity is a phone book, not a trust anchor.

## Nothing is deleted

There is no delete. Revocation appends a new entry saying the previous binding
no longer holds, and the record stays in the log with `state: revoked` and a
reason.

This is not squeamishness about data. It is the requirement:

- **A revoked record must stay distinguishable from one that never existed.**
  "This key was valid and was withdrawn on the 14th" and "I have never heard of
  this key" are different facts, and a client that cannot tell them apart cannot
  tell a rotation from an attack.
- **Historical signatures must stay verifiable.** A message signed in June, by a
  key revoked in August, was validly signed. Deleting the key would make a
  correct signature permanently unattributable.

So a revoked record still resolves. `include_revoked=true` returns it with its
state, and `as_on` answers what was true at any instant.

## What downstream actually does with it

Two things, and only the second is automatic:

**Signature validation stops.** A validator that re-fetches the key sees the
record is not live and refuses. This is the mechanism that matters.

**Discovery stops returning them.** Since the domain query landed, a revoked
participant is no longer returned as a destination — see
[discovery](/docs/discovery). Before that, revocation meant "stops being
verifiable" but not "stops being routed to", which is a strictly weaker claim
than it sounds.

Note what revocation does *not* do: it does not reach into anybody's cache, and
it does not stop a counterparty who never re-checks. Which brings us to timing.

## How long revocation takes

Measured end to end against two live beckn-onix v1.8.0 adapters, with the
participant record declaring `ttl: 20`:

```
t+00s  search accepted (adapter still holds the cached key)
t+05s  search accepted
t+10s  search accepted
t+15s  search REJECTED -> HTTP 401
```

**Fifteen seconds during which a compromised key kept validating real traffic.**

TTL *bounds* that window. It does not remove it, and it is the only thing
bounding it unless [push](/docs/push) is configured, which delivers the
revocation instead of waiting for a timer.

Lowering TTL is not a free fix: it trades the exposure window for lookup load on
every validator on every message.

## Doing it

The write is conditional, like every other. Read the current version tag and
send it, so a concurrent change fails loudly rather than being clobbered:

```sh
echo '{"payload":{"reason":"key compromised, reported by operator 2026-08-14"}}' > reason.json

t=$(curl -s "$DEDI/dedi/lookup/$NS/$REG/$NAME?include_revoked=true" | jq -r '.data.version_tag // empty')

dedid sign -key publisher.key -kid op-1 \
  -method POST -path /admin/namespaces/$NS/registries/$REG/records/$NAME/revoke \
  -body reason.json -if-match "$t" -curl
```

### Write a real reason

The reason is published and permanent, and it is the only part of a revocation a
future reader cannot reconstruct from the log itself. "compromised" tells them
nothing. What happened, who reported it, and when — that is what someone
auditing this in a year needs, and they cannot ask you.

## Revoke every binding, not one

A subscriber may hold several keys, each its own record
([onboarding](/docs/onboarding)). Revoking one binding leaves the others live.

That is correct behaviour and a real trap. On the demo network a participant
existed twice — once under its key id, once under its `subscriber_id` — with
identical payloads. Nothing failed, because lookup matches on the payload and
never reads the record name. Revoking the key-named record left the other live
and still being handed out as a destination.

So before revoking, list what the subscriber actually holds:

```sh
curl -s "$DEDI/dedi/query/$NS/$REG?page_size=100" \
  | jq -r '.data.records[].record_name' \
  | while read n; do
      curl -s "$DEDI/dedi/lookup/$NS/$REG/$n" \
        | jq -r --arg n "$n" '"\($n)\t\(.data.details.subscriber_id)\t\(.data.state)"'
    done
```

Anything sharing the `subscriber_id` you are removing needs revoking too.

## Verifying it worked

Not "the write returned 200". Check the three things that are separately true:

```sh
# 1. the record reports revoked, and is still there
curl -s "$DEDI/dedi/lookup/$NS/$REG/$NAME?include_revoked=true" | jq '.data.state'

# 2. it is gone from discovery
curl -s "$DEDI/dedi/query/$NS/$REG?domain=$DOMAIN" | jq '.data.participants[].subscriber_id'

# 3. a real signed message from that key is now refused by a counterparty
```

(3) is the one that counts, and the only one that measures the property
revocation exists to deliver. The other two say the registry changed its mind;
only the third says the network did.

## Undoing one

Publish a new binding. There is no "un-revoke", because the revocation happened
and the log says so permanently. Bringing a participant back is a new statement
about the future, not an edit to the past — which is what you want, since the
readers who need to know they were once removed are exactly the ones who would
be misled by it disappearing.
