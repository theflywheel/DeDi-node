# Key rotation

Replacing the key a participant signs with, without removing the participant.
The console's *Rotate a key* tab, and the reasoning about time that makes a
rotation safe rather than an outage.

## Rotation is two events, not one

Naively, rotation is "change the key". In a live network it is two things that
happen at different times:

1. the participant **starts** signing with the new key
2. every counterparty **stops** expecting the old one

Between them is a window, and which order you put them in decides what breaks.

Publish the new key first and the participant's old signatures keep verifying
until caches expire — nothing breaks, and there is a period where either key is
accepted. Revoke the old key first and every message in flight signed with it
fails, in a network you do not control, for a duration set by other people's
retry logic.

So: **publish, let it propagate, then revoke.** The overlap is the feature.

## Why the record name is the key id

Records are named by key id ([onboarding](/docs/onboarding) says why), which is
what makes rotation expressible at all. A rotation is:

- a new record, named for the new key id, live
- the old record, named for the old key id, revoked when the window closes

Both bindings belong to the same `subscriber_id`, both are in the log, and the
history says exactly when each was valid. A registry that stored one key per
participant could not represent the overlap, and would have to choose between an
outage and a lie.

## How long the window should be

Long enough for every cache to turn over. The bound is `ttl` on the record: a
validator that fetched the old key can keep using it for that long, so the
window must be at least one full TTL, and in practice a comfortable multiple of
it.

Measured on the demo network, with `ttl: 20`, a revocation took **15 seconds**
to stop a correctly-signed message. That is the number to reason with — the
staleness is bounded by TTL, and TTL is the only thing bounding it, unless you
have [push](/docs/push) configured, which removes the window instead of
bounding it.

Lowering TTL to shorten the window is not free: it trades exposure for lookup
load on every validator, on every message.

## The steps

```sh
# 1. the participant generates a new keypair and gives you the PUBLIC half
#    (if you generated it for them, you now know their private key — do not)

# 2. publish the new binding, named for the new key id
dedid sign -key publisher.key -kid op-1 \
  -method POST -path /admin/namespaces/$NS/registries/$REG/records/$NEW_KID/publish \
  -body new-binding.json -create -curl

# 3. confirm it resolves before touching anything else
curl -s "$DEDI/dedi/lookup/$NS/$REG/$NEW_KID" | jq '.data.details.signing_public_key'

# 4. the participant switches to signing with the new key,
#    and you verify a real message verifies against the registry

# 5. only now, revoke the old binding
dedid sign -key publisher.key -kid op-1 \
  -method POST -path /admin/namespaces/$NS/registries/$REG/records/$OLD_KID/revoke \
  -body reason.json -if-match "$(...)" -curl
```

Step 4 is not optional and is the one people skip. Publishing a key proves the
registry accepted bytes. It does not prove the participant is signing with the
matching private half. Until one real message signed by the new key verifies
against what the registry serves, you have not rotated anything — you have
published a claim.

## Compromise is a different procedure

Everything above assumes an orderly rotation. If a key is believed
**compromised**, the overlap is the attacker's window, not a safety margin.

Revoke first, accept the breakage, then publish the replacement. A compromised
key that keeps validating for one more TTL is a compromised key that keeps
validating; the disruption is the cheaper half of that trade, and it is
recoverable.

This is exactly the case [push](/docs/push) exists for. With a webhook
subscription the revocation is delivered rather than waited for, so the
exposure window is closed by a signed push instead of by a timer.

## What the log ends up saying

After a rotation the log holds both bindings and their whole history: when the
new key appeared, when the old one was withdrawn, and who did each. Anyone
verifying a signature from last month can establish which key was live at that
instant, using `as_on`:

```sh
curl -s "$DEDI/dedi/lookup/$NS/$REG/$OLD_KID?as_on=2026-07-01T00:00:00Z"
```

That is the property a rotation must not destroy, and the reason nothing is
deleted. A registry that overwrote the key would make every historical signature
unverifiable — correct signatures on real messages, permanently unattributable,
because the evidence was tidied away.
