# Onboarding a participant

Admitting an identity to a registry: the Participants section's *Onboard a participant* disclosure in the console,
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

These are the payload field names ONIX's `dediregistry` client reads, and the
ones the console writes.

| Field | Notes |
|---|---|
| `subscriber_id` | The identity as the network names it, e.g. `bpp.example.com`. |
| `type` | `BAP`, `BPP`, `BG` or `CDS`, the enum in the standard's `Beckn_subscriber` schema. The console offers the first three. |
| `status` | `SUBSCRIBED` for a live participant. The Beckn lookup and discovery answer only for `SUBSCRIBED` or no status at all. |
| `url` | Where the network reaches them. |
| `domain` | What they serve, e.g. `retail`. Feeds [discovery](/docs/discovery). |
| `network_memberships` | Which networks this binding is valid on. ONIX rejects a sender whose list does not include its own network. |
| `signing_public_key` | The Ed25519 key signatures are checked against, standard base64. |
| `encr_public_key` | The X25519 encryption key. |
| `valid_from` / `valid_until` | Optional; the binding's own lifetime. A lookup reports `expired` / `not_yet_valid` against them, but still answers. |
| `ttl` | Optional; seconds a client may cache this record. Overrides the node's `DEDI_TTL`. |

The key id is not a field: it is the record's **name**.

### The record name is the key id, not the subscriber

This trips people up, so it is worth stating plainly: records are named by **key
id**, the id the participant's adapter sends with every signature. ONIX looks a
sender up as

```
GET /dedi/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}
```

and the node answers it by finding the latest live record **named `{key_id}`**
whose payload `subscriber_id` (or namespace) is `{subscriber_id}`, in a
namespace allowed to answer it (`DEDI_WILDCARD_NAMESPACES`). A record named anything else is
never found by that lookup. One subscriber may hold several keys, and each
binding gets its own record so each can be revoked on its own.

Discovery (`?domain=`), on the other hand, matches on the payload and ignores
the name. So a record mistakenly named after the subscriber sits alongside the
correct one, invisible to signature validation but returned by discovery: the
participant is listed twice, and revoking the key-named record leaves the other
still handed out as a destination. We shipped that bug on the demo network.

### The payload must be flat

The Beckn wildcard lookup matches `subscriber_id` at the **top level** of the
payload. A record whose fields are nested under `details` resolves for nobody.

`details` in the *response* is a projection the read plane builds. It is not the
shape you publish.

### Required by our own schema

`countries` is required by the standard's `Beckn_subscriber` schema, which this
node ships as a [built-in reference schema](/docs/reference-schemas). A
registry created with `"schema": "builtin:Beckn_subscriber"` rejects a record
without it, and the conformance suite's beckn profile checks for it. The
console's form does not ask for it, so add it when you publish into such a
registry. ISO 3166-1 alpha-3.

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
  -body party.json -create > headers.txt

curl -s -u "admin:$DEDI_ADMIN_PASSWORD" -X POST \
  "$DEDI/admin/namespaces/$NS/registries/$REG/records/$KEY_ID/publish" \
  -H @headers.txt -H 'Content-Type: application/json' --data-binary @party.json
```

`-create` says the record must not exist yet; re-publishing one that does needs
`-if-match`, below.

**Never hardcode the public key.** Read it from the participant, every run. Ours
were literals in a seeding script; the adapters' keys were rotated; the script
went on publishing keys the signers had stopped using. Every write returned 200,
every record read back looking correct, and the whole network failed one layer
away with `401 Signature Validation Error`.

## Publishing twice is how you reconcile, not something to avoid

The write is a conditional upsert. Read the record's current version tag and
send it as `-if-match`; send `-create` when it does not exist yet:

```sh
t=$(curl -s "$DEDI/dedi/lookup/$NS/$REG/$KEY_ID?include_revoked=true" | jq -r '.data.version_tag // empty')
[ -n "$t" ] && pre="-if-match $t" || pre="-create"
```

That makes re-running a seed the way you bring the registry back in line with
the file that describes it, and it makes a concurrent change someone else made
fail loudly instead of being silently overwritten.

## Verify what landed, not that it was accepted

A 200 means the write was signed correctly and appended. It does not mean the
contents are right. Read the record back and check the field that matters:

```sh
curl -s "$DEDI/dedi/lookup/$NS/$REG/$KEY_ID" \
  | jq '.data.details | {subscriber_id, type, url, signing_public_key}'
```

Then check it end to end: have the participant sign something and confirm a
counterparty verifies it. That is the only test of an onboarding that measures
the thing onboarding is for.
