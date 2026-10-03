# Use case: CREST

[CREST](https://github.com/theflywheel/CREST) issues verifiable credentials for
work: who did what, under which terms, on whose authority. A credential is only
as good as the facts behind it, so CREST publishes those facts to a DeDi node
and hands verifiers a way to check them without asking CREST.

## The deployment

CREST runs its own node, not a namespace on someone else's:
`https://crest-dedi-production.up.railway.app`. Its log, origin and key mean
"CREST" and nothing else, which is what a verifier checks against. Everything is
under the namespace `crest`:

| Registry | Holds |
|---|---|
| `work-definitions` | every active work definition |
| `organisations` | approved organisations |
| `terms` | the terms work is done under |
| `authorizations` | what each organisation is authorized to attest |
| `skills` | the skill list, as reference data |
| `instances` | the deployment's own self-description: who operates it, which publisher key its writes carry |

CREST's services write through the signed write plane with their own publisher
key, scoped to `crest`.

The node is witnessed by a second node, `dedi-global-production.up.railway.app`,
and witnesses it back. Both verdicts are public:

```sh
curl -s https://dedi-global-production.up.railway.app/dedi/witness | jq .data.targets
curl -s https://crest-dedi-production.up.railway.app/dedi/witness | jq .data.targets
```

## What a verifier gets

CREST's verifier walks a credential's trust chain, from the work definition up
through the organisation, its authorization and the deployment, and reports
each link. A link backed by the node is reported as checkable, with the exact
URL to check it:

```
https://crest-dedi-production.up.railway.app/dedi/lookup/crest/<registry>/<record>?version_id=<id>&proof=inclusion
```

Two parameters carry the weight. `version_id` pins the exact version CREST
published and recorded, so a later edit cannot change what the link is checked
against, and the earlier version keeps resolving. The
inclusion proof shows that version is in the log under a checkpoint signed by
the node. Anyone can fetch that URL and check it with no CREST code at all.

## What CREST checks itself

An inclusion proof says a record is in the log at the root the node serves
today. It does not say the log was not rewritten to produce that root. The
rest of what the node offers closes that gap, and CREST is adding it to its
`pkg/dedi` package in an open change (CREST pull request #242), not yet on its
main branch. Once merged:

- **Checkpoints, authenticated.** It verifies each checkpoint against the
  node's verifier key with the same `sumdb/note` code the node signs with.
- **Consistency.** It pins the newest checkpoint it has authenticated, and
  refuses any later one that is not an append-only extension: smaller, or
  same size with a different root, or failing the consistency proof from the
  pin. The pin is persisted, so a rewrite while CREST was down is caught on
  the next start.
- **Witness verdicts.** It asks the witness for its verdict on the node's
  origin at `/dedi/witness/{origin}` and reads `witnessed` and
  `consistency_ok` separately. "Never checked" is reported as not established,
  never as sound.

## Why DeDi fits

CREST needs a registry its own operators cannot quietly rewrite, that
verifiers can read without credentials, and that keeps old versions answerable
by id. Those are exactly the log's properties. The standard's read API means a
verifier needs no CREST-specific client to look a fact up; the proof means it
does not have to believe the answer.
