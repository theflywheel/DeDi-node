# Onboarding a participant

Admitting an identity to a registry: the console's *Onboard a participant* tab,
and the signed write behind it. This is the operation that decides who the
network will believe, so it is operator-gated and there is no self-service path.

Policy — who *should* be admitted, and on whose authority — is
[governance](/docs/governance). This page is the mechanics.

## What a participant record is for

A record binds a `subscriber_id` to the public keys that identity signs with,
plus enough metadata for a router to act on it. When an ONIX adapter receives a
signed message it looks up the sender here and verifies the signature against
what it finds. That single lookup is the reason the registry exists.

Which means the failure that matters is not "the record is missing" — that fails
loudly and gets fixed. It is **the record is present and wrong**: a stale key
publishes an identity nobody can verify, and it is indistinguishable from that
participant being compromised. Getting a key right is more important than
getting a record in quickly.

## The fields

| Field | Notes |
|---|---|
| `subscriber_id` | The identity as the network names it, e.g. `bpp.example.com`. |
| `role` | `BAP`, `BPP`, `BG`, `DS`, `PROVIDER`. |
| `status` | `SUBSCRIBED` for a live participant. The read plane's Beckn lookup filters on it. |
| `url` | Where the network reaches them. |
| `domain` | What they serve, e.g. `retail`. Feeds [discovery](/docs/discovery). |
| `network_memberships` | Which network this binding is valid on. |
| `key id` | Names *this* key, so a later rotation can supersede exactly one. |
| `signing_public_key` | The key signatures are checked against. |
| `encryption_public_key` | Usually the same value in Beckn deployments. |
| `valid from` / `valid until` | Optional; the binding's own lifetime. |

### The record name is the key id, not the subscriber

This trips people up, so it is worth stating plainly: records are named by **key
id**. One subscriber may hold several keys, and each binding gets its own record
so each can be revoked on its own. Revoking a compromised key should not remove
a participant's other, uncompromised keys.

Lookup never depends on the record name — the Beckn path matches
`payload->>'subscriber_id'` at the top level — which is exactly why a record
mistakenly named after the subscriber can sit alongside the correct one, serving
the same participant twice, and nothing fails. Discovery returns the participant
twice, and revoking one leaves the other live and still being handed out. We
shipped that bug on the demo network; see
[beckn-demo](/docs/beckn-demo).

### The payload must be flat

The Beckn wildcard lookup matches `subscriber_id` at the **top level** of the
payload. A record whose fields are nested under `details` resolves for nobody.

`details` in the *response* is a projection the read plane builds. It is not the
shape you publish.

### Required by our own schema

`countries` is required by `schemas/Beckn_subscriber.json`, which this node
serves as a [built-in reference schema](/docs/reference-schemas). Omitting it
publishes a record our own registry would reject — the conformance suite catches
it, and it should not have taken the suite to notice. ISO 3166-1 alpha-3.

## Doing it from a script

The console is convenient for one participant. For a set you want to be
reviewable and repeatable, sign the same request from a shell:

```sh
cat > party.json <<JSON
{"payload":{
  "subscriber_id":"bpp.example.com",
  "url":"https://bpp.example.com/beckn",
  "type":"BPP",
  "domain":"retail",
  "countries":["IND"],
  "signing_public_key":"$PUBKEY","encr_public_key":"$PUBKEY",
  "status":"SUBSCRIBED",
  "network_memberships":["beckn.one/testnet"],
  "ttl":300}}
JSON

dedid sign -key publisher.key -kid op-1 \
  -method POST \
  -path /admin/namespaces/$NS/registries/$REG/records/$KEY_ID/publish \
  -body party.json -curl
```

**Never hardcode the public key.** Read it from the participant, every run. Ours
were literals in a seeding script; the adapters' keys were rotated; the script
went on publishing keys the signers had stopped using. Every write returned 200,
every record read back looking correct, and the whole network failed one layer
away with `401 Signature Validation Error`.

## Publishing twice is how you reconcile, not something to avoid

The write is a conditional upsert. Read the record's current version tag and
send it as `-if-match`; send `-create` when it does not exist yet:

```sh
t=$(curl -s "$DEDI/dedi/lookup/$NS/$REG/$NAME?include_revoked=true" | jq -r .data.version_tag)
[ -n "$t" ] && pre="-if-match $t" || pre="-create"
```

That makes re-running a seed the way you bring the registry back in line with
the file that describes it, and it makes a concurrent change someone else made
fail loudly instead of being silently overwritten.

## Verify what landed, not that it was accepted

A 200 means the write was signed correctly and appended. It does not mean the
contents are right. Read the record back and check the field that matters:

```sh
curl -s "$DEDI/dedi/lookup/$NS/$REG/$NAME" \
  | jq '.data.details | {subscriber_id, type, url, signing_public_key}'
```

Then check it end to end: have the participant sign something and confirm a
counterparty verifies it. That is the only test of an onboarding that measures
the thing onboarding is for.
