# Governance (v0)

How identities get into — and out of — a dedid registry. Status: v0, deliberate
minimum. The signed write plane carries out these decisions; it does not make
them.

## Principle: closed by default, auditable always

A registry's power is deciding who exists on the network, so onboarding is a
**governed act, not an API call**. Two invariants:

1. **Closed by default.** No self-service registration. The read plane is
   public; the write plane does not exist until the operator configures a
   publisher key, and then accepts only writes signed by one.
2. **Every governance decision is a log entry.** Onboarding, key rotation, and
   revocation are appends to the transparency log — provable, ordered,
   witnessable. Governance that cannot be audited is not governance.

## v0 workflow (current)

- **Apply:** email the operator — for the reference nodes,
  **contact@theflywheel.in** — with: who you are, the network/namespace you
  want to join, your subscriber id, type (BAP, BPP, BG or CDS), and your
  Ed25519 signing public key (base64, raw 32 bytes).
- **Verify:** the operator verifies the requester controls the claimed
  subscriber domain and key out-of-band before publishing anything.
- **Publish:** the operator publishes the participant record with a signed
  write, from the [console](operator-console.md) or a script
  ([onboarding](onboarding.md)). The new identity is live at once, and covered
  by the next checkpoint and an inclusion proof like any other record.
- **Revoke:** same path, in reverse — a revocation is a new version with a
  revoked state, never a deletion; history stays provable.

Enforcement is structural. A node with no `DEDI_PUBLISHER_KEYS` registers no
write routes at all. A node with keys has 13 signed `/admin` routes, and each
accepts only a request signed by a key the operator issued, scoped to one
namespace, so an unsolicited party cannot onboard even by trying. (`dedid seed`
also writes, but straight to the database, so it needs database access.)

## Authentication on the admin surface

Two independent checks, answering different questions. Both must pass.

| | Question it answers | Configured with | Scope |
|---|---|---|---|
| **Operator gate** (HTTP basic) | May you reach this node's admin surface at all? | `DEDI_ADMIN_USER` (default `admin`), `DEDI_ADMIN_PASSWORD` | The whole surface: the console page and every `/admin/**` route |
| **Publisher signature** (Ed25519) | Who is writing, and may that key write here? | `DEDI_PUBLISHER_KEYS` | Per key, per namespace |

The gate never replaces the signature. A shared password cannot say *who*
wrote — it is the signature that binds a version to a key id, records
`created_by: publisher:<kid>`, and confines a key to its own namespaces. So
operator credentials alone cannot write; a signature alone cannot get through a
configured gate.

The gate is optional. Making it mandatory would lock out every running
deployment and signed script on the next restart, and the write plane was
already closed to anything unsigned. Leaving it unset with the write plane open
logs a warning on every boot, because the console is then a publicly reachable
form soliciting a private key.

Two structural properties matter as much as the checks:

- **A read-only node serves no console and registers no write routes.** With no
  publisher keys configured there is nothing to authenticate against, so the
  surface does not exist rather than existing and refusing.
- **The console holds no privilege.** It reads public endpoints; the key an
  operator pastes is imported non-extractable, kept in a closure, and never put
  in `localStorage`, a cookie, or a request body. The page is served with a
  strict CSP, `frame-ancestors 'none'` and `Cache-Control: no-store`, because
  the pasted key is the thing an attacker would want out of that origin.

## What is built, and what is not

- **Operator-granted publisher keys: built.** `dedid pubkeygen` mints a key
  scoped to one namespace; the operator adds its public half to
  `DEDI_PUBLISHER_KEYS`. Every version records `created_by: publisher:<kid>`.
  The grant itself lives in the node's configuration, not in the log.
- **Wildcard eligibility: built.** Only the namespaces in
  `DEDI_WILDCARD_NAMESPACES` may answer the Beckn wildcard lookup and domain
  discovery, so a publisher key is never an implicit grant of network-wide
  identity authority.
- **Delegation: built.** A namespace can be granted to another operator's node,
  with the grant in the log ([delegation](delegation.md)).
- **Multi-party approval (k-of-n operator signatures): not built.**

## Non-goals

- Token-based or stake-based governance.
- Anonymous self-registration in any mode.
