# Child nodes and namespace delegation

**Status:** implemented (2026-08-08). Adds a third relationship between nodes,
alongside the witness ring ([witnessing](witnessing.md)) and the Raft cluster
([replication](replication.md)). A child is one of the five node roles; the
console's **Add a node** tab renders all five, and only a child is minted a
delegation.

---

## Three relationships, deliberately kept apart

A node can now stand in three different relations to another node, and the
whole design rests on not confusing them.

| | Raft replica | Witness peer | **Child node** |
|---|---|---|---|
| Identity | this node's key | a stranger's key | **its own key** |
| Log | this log, copied | its own, unrelated | **its own, delegated** |
| Operator | the same one | someone else | **someone else** |
| Buys you | uptime | tamper evidence | **a namespace someone else runs** |
| Proof it carries | none | consistency proofs | inclusion proof of the grant |

A child is the only one of the three that is both *independently verifiable*
and *related*. A replica shares the parent's key, so it can prove nothing about
the parent. A witness shares no authority, so there is nothing to delegate. A
child has its own key — meaning its history can be checked without the parent
in the loop — while holding a namespace the parent granted it.

---

## The parent never holds the child's private key

The convenient design is for the parent to generate the child's identity
keypair and hand over a complete bundle: one click, nothing for the operator to
do. It is also the design that quietly destroys the property the child exists
to have.

A parent that has held the child's private key can sign checkpoints as the
child, indefinitely and undetectably. The child's log then proves exactly
nothing that the parent's own word did not already assert, and every relying
party checking the child's signature is checking a signature the parent could
have produced. "Independently verifiable child" becomes decoration.

So enrolment is two steps:

```
  operator                parent node                      child node
     │                        │                                │
     │─ mint offer ──────────►│                                │
     │                        │─ append {state: offered,       │
     │                        │          token_hash} to log    │
     │◄─ token + config ──────│                                │
     │                                                         │
     │─ apply config ─────────────────────────────────────────►│
     │                                            generates its own key
     │                        │◄─ POST /enrol {token, key, url}│
     │                        │─ append {state: active,        │
     │                        │          child_key} to log     │
     │                        │─ start witnessing the child ──►│
```

The private key never crosses the boundary. The parent's log records the child's
*public* key, which is exactly what a third party needs to verify the child
without asking the parent anything.

---

## Delegation lives in the log

Every fact — the offer, the enrolment, a later revocation — is a record in the
parent's transparency log under the reserved registry `_delegations`, one record
per child, versioned like any other.

That is not filing preference. "Who granted this node authority over this
namespace, and when?" is precisely the class of question the log exists to
answer with evidence: a relying party can demand an inclusion proof against a
signed checkpoint that witnesses have already checked. A side table would
answer the same question with the operator's word, which is the thing this
project spends all its effort not requiring.

It also means delegation replicates for free. On a clustered node the offer and
the enrolment go through Raft like any other write, and every replica holds the
same delegation state.

### The token is never in the log

The log is public, unauthenticated, replicated to every replica, and mirrored by
witnesses. An enrolment token in it is the namespace handed to every reader. The
log holds `sha256(token)`; the plaintext exists only in the single API response
that mints it, and is not recoverable. Losing it means minting a new offer.

On redemption the hash is cleared rather than left behind, so a spent offer
leaves nothing to match against.

---

## What stops a namespace being taken

Four independent things, because this is the part where a mistake hands someone
else's namespace away:

1. **A parent can only delegate one level under itself.** `beckn` may grant
   `beckn.mobility`; it may not grant `onix.mobility`, `beckn`, or
   `beckn.mobility.metro` — the last belongs to `mobility` to grant. Checked
   before anything is signed.
2. **Minting is a signed publisher write**, scoped to the parent namespace.
   Granting away a slice of a namespace is at least as consequential as
   publishing in it.
3. **An offer is single use and expires within the hour.** A second redemption
   fails on the state check; a late one fails on expiry.
4. **The redeeming append carries a precondition** on the version it read. Two
   children racing the same leaked token cannot both enrol — the second write
   is refused rather than silently replacing the first as the holder.

An active delegation also cannot be re-minted over. Re-issuing would hand the
namespace to whoever redeemed the new token, which is a takeover of the child
wearing a convenience's clothing; it must be revoked explicitly first.

---

## Revocation

`POST /admin/namespaces/{ns}/children/{child}/revoke`, signed and scoped
exactly like minting — the authority to grant a slice of a namespace and the
authority to take it back are the same authority, exercised in two directions.

It applies to an unredeemed offer as well as to an enrolled child, because
those are one problem in two costumes: an outstanding offer is a bearer
credential for the namespace, and *"I minted that by mistake"* needs a better
answer than waiting out `TokenTTL` and hoping nobody found the token first.

**What revocation does not do is reach into the child.** The child keeps its
key, its database and its log, and goes on serving; this node has no authority
over another operator's process, and an API that implied otherwise would be
lying about what the mechanism is. What changes is that this node's log now
records, with a timestamp and under the same signature as everything else, that
the authority it granted is withdrawn — and relying parties check the grant.
The admin console says this in the confirmation prompt, because the intuitive
reading of a Revoke button is that it turns the child off.

Three things follow:

- **The witness loop stops.** A parent still publishing verdicts about a child
  it has stopped vouching for reads downstream as the parent standing behind it
  after all, and the child is dropped from the network view rather than left as
  a green row nobody checks.
- **The child's identity is kept, not erased.** Who held the namespace and until
  when is exactly what an audit of an older signature from that child depends
  on. Deleting it on revocation would destroy the evidence at the moment it
  starts to matter.
- **The namespace becomes re-delegatable.** Revocation is the only path out of
  a live delegation, so a revocation that did not free the namespace would leave
  the operator exactly as stuck as having no revocation at all.

### A stale offer has two exits

`offered` is two situations under one label — a child that has not booted yet,
and an offer nobody will ever redeem — and they call for opposite responses.
The delegation list therefore publishes `expires_at` and `expired` for
unredeemed offers (never the token hash; the surface is unauthenticated), and
the console renders "expires in 42m" or "expired, never enrolled".

From there:

- **Discard it** — revocation, which covers offers precisely because an
  outstanding one is a bearer credential. The console labels it *discard*
  rather than *revoke* for an offer, because they are the same call but not the
  same act, and one word for both makes the destructive one look routine.
- **Mint over it.** Permitted for an offer, refused for an active delegation.
  The new offer replaces the record, so the superseded token stops working —
  otherwise re-minting would double the number of credentials that can claim
  the namespace rather than replacing one.

The redeeming and revoking writes both carry a precondition on the version they
read, which covers the mirror-image race: a revocation must not silently
overwrite an enrolment that landed a moment earlier and leave a record saying
"revoked" about a child the parent is meanwhile happily witnessing.

→ `internal/delegation/delegation.go` (`Revoke`), `internal/api/children.go`
(`revokeChild`)

→ `internal/delegation/delegation_test.go`, `internal/api/children_test.go`

---

## The parent witnesses its children

Once enrolled, the parent runs a witness loop against each child: fetching its
checkpoints, verifying append-only consistency, and recording verdicts in its
own log. This is what turns the delegation from a claim into something worth
relying on — not merely "I granted them this namespace" but "and I am
continuously checking they have not rewritten what they did with it".

It is deliberately **not** mutual, and it is **not** a substitute for the open
ring. A child witnessing its parent would be witnessing the node that granted
its authority — the least independent observer available. Children should be
witnessed by unrelated operators like anyone else. This is an extra check by an
interested party, and it should be read as exactly that.

Witnessing resumes across restarts for every child already delegated, so a
redeploy does not silently stop checking them.

### The verdict and the health of the loop are different claims

`GET /dedi/delegations/{ns}` carries both per active child, and the console
shows them together, because either one alone misleads:

| | what it is | who vouches for it |
|---|---|---|
| **Verdict** — size, root, `consistency_ok` | evidence | nobody: it is backed by a consistency proof checkable against the child's own key |
| **Health** — checking, stale, last error | operational signal | this node, about itself — not evidence of anything |

The reason they cannot be separated is that a verdict is only rewritten when the
child's tree changes. A witness loop failing on every run for hours keeps
displaying its last verdict, still reading `consistency_ok`, indistinguishable
from a check that ran a second ago and found nothing new. Verdict age cannot
substitute for the health signal either: on a quiet child the newest verdict is
legitimately old.

Reachability sits beside them as a third, separate thing — the console shows
`up` or `unreachable` per child so an operator no longer has to open the
explorer page to learn whether the node answers at all. It is kept visibly apart
from the verdict: a node being up proves nothing about its log, and a node
briefly down disproves nothing about what was already verified. Rendering them
alike would let uptime read as verification, which is the confusion the whole
design exists to avoid.

A child the log lists as active with no loop running at all is reported as
stale rather than silently omitted — most often a child enrolled before a
restart that `Resume` did not pick up, which otherwise shows only as a verdict
frozen at whatever it last said.

→ `internal/api/children.go` (`childWitness`), `cmd/dedid/children.go`
(`childSupervisor.Health`)

---

## Provisioning: rendered, not executed

`POST /admin/namespaces/{ns}/children` returns a deploy artifact alongside the
token. Providers ship for `env`, `compose`, `railway` and `pulumi`, and
`provision.Register` takes more.

**Every provider renders text. None calls a cloud API, and the daemon holds no
cloud credential.** A registry daemon holding a token that can create and
destroy infrastructure has a blast radius wildly out of proportion to its job:
the same process serving public unauthenticated lookups could, if compromised,
delete the cluster. Handing back a manifest keeps the credential with the
operator and their existing deploy path, which already has review, audit and
rollback that a POST from a web form does not.

The `Provider` interface is the seam. One that *does* execute — Pulumi's
Automation API, a Terraform runner, an internal control plane — implements the
same interface; it simply also needs credentials, and that decision stays with
whoever wires it in.

---

## Operating it

**On the parent**, nothing to configure beyond `DEDI_PUBLIC_URL`. Wherever the
write plane is open, `/admin` has two tabs for this: **Add a node** mints the
offer and renders the child's configuration (as `env`, `compose`, `railway` or
`pulumi`), and **Child nodes** lists what has been delegated, with each
child's verdict, loop health and reachability, and the revoke or discard
control. The API behind them is `POST /admin/namespaces/{ns}/children` and
`…/children/{child}/revoke`; the child redeems its offer at `POST /enrol` on
the parent (`POST /dedi/enrol` is a deprecated alias).

| Variable | Meaning |
|---|---|
| `DEDI_PUBLIC_URL` | this node's externally reachable base URL — what children are told to enrol against. Behind a proxy the request Host is the proxy's, so it cannot be inferred |
| `DEDI_CHILD_WITNESS_INTERVAL` | how often to verify each child (default `60s`) |

**On the child**, the rendered artifact sets these:

| Variable | Meaning |
|---|---|
| `DEDI_PARENT_URL` | where to enrol |
| `DEDI_ENROL_NAMESPACE` | the namespace being claimed |
| `DEDI_ENROL_TOKEN` | the one-time offer secret |
| `DEDI_PUBLIC_URL` | where the parent can reach the child to witness it |

Enrolment runs in the background and retries. A child usually boots before its
own DNS resolves, and giving up after one attempt would leave a node that is
healthy, serving, and quietly not delegated — the failure most likely to be
mistaken for success, because every surface an operator checks looks fine. A
child that has already enrolled detects that and stops, so a restart does not
retry against a spent offer forever.

Without `DEDI_PUBLIC_URL` the child declines to enrol rather than registering an
address nobody can reach.

### What the child does for itself on enrolment

On success the child creates the delegated namespace **in its own log**,
recording the parent that granted it and the parent's verifier key. Without
that step the child comes up enrolled, healthy, and rejecting every write with
a missing-namespace error — which is only visible if you actually try to
publish something. It was found by running a real child against a real parent,
not by reading the code.

**The child gets no publisher key.** The rendered config deliberately omits
`DEDI_PUBLISHER_KEYS`: the child's write plane belongs to the child's operator,
and a parent handing over publishing credentials would be a second version of
the private-key mistake above. Until its operator generates one, the child
serves reads and accepts no writes.

---

## Chains

Nothing stops `beckn.mobility` delegating `beckn.mobility.metro`: the one-level
rule is enforced relative to whoever is granting, so a chain grows one link at a
time, each link granted by the holder of the link above. `beckn` cannot mint
`beckn.mobility.metro` even holding a key for both namespaces — the route's
namespace is `beckn`, and that is two levels down.

The console walks a chain a level at a time, and **each hop is read from that
node directly, not through this one.** A hierarchy assembled here from what a
parent says about its grandchildren would be the parent's word about a log it
does not hold; read from the node itself it is that node's own answer under its
own key. The child's read plane is public and CORS-open precisely so a browser
can do this without anyone's cooperation. One level per click rather than a
recursive crawl, so a stalled node deep in a chain does not hold up the view of
everything above it.

### Revocation is not transitive

When `beckn` revokes `beckn.mobility`, the record `beckn.mobility` holds for
`beckn.mobility.metro` is untouched and still reads `active`. That is correct —
a node can only speak about grants it made, and rewriting someone else's log is
not available to it — but it means **a relying party must walk the whole chain**.
A grandchild whose parent has lost its delegation still looks live if you ask
only the grandchild.

Verified end to end with three real nodes: `beckn` → `beckn.mobility` →
`beckn.mobility.metro`, each enrolled with its own key and witnessed by the
level above; revoking the middle link stopped the root's witness loop, kept the
child's identity in the root's log, and left the bottom link untouched.

---

## Known gaps

- **Revocation is not pushed to the child.** It is published, so a party that
  re-checks sees it. [Push](push.md) can carry it to anyone who asks: grants
  are records in the parent namespace's `_delegations` registry, so a webhook
  subscription on that registry is notified of every grant, enrolment and
  revocation. Nothing subscribes the child or its relying parties
  automatically.
- **The child does not verify the parent.** `DEDI_PARENT_KEY` is recorded in
  the child's own namespace (`parent_key`) but never used to check the parent's
  log; a child could and should witness that its parent's log is append-only,
  notwithstanding the independence caveat above — it is a weak check, but a
  cheap one.
- **Nothing validates a chain end to end.** The console walks it a level at a
  time and an operator can see every link, but no code answers "is every link
  above this node still live?" — see the non-transitivity note above. That check
  belongs to the relying party, and there is nothing here to help it yet.
