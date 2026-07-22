# Governance (v0)

How identities get into — and out of — a dedid registry. Status: v0, deliberate
minimum. The publisher plane (M2) builds on this; it does not replace it.

## Principle: closed by default, auditable always

A registry's power is deciding who exists on the network, so onboarding is a
**governed act, not an API call**. Two invariants:

1. **Closed by default.** No self-service registration. The read plane is
   public; the write surface does not exist outside operator tooling.
2. **Every governance decision is a log entry.** Onboarding, key rotation, and
   revocation are appends to the transparency log — provable, ordered,
   witnessable. Governance that cannot be audited is not governance.

## v0 workflow (current)

- **Apply:** email the operator — for the reference nodes,
  **contact@theflywheel.in** — with: who you are, the network/namespace you
  want to join, your subscriber id, role (BAP/BPP), and your Ed25519 signing
  public key (base64, raw 32 bytes).
- **Verify:** the operator verifies the requester controls the claimed
  subscriber domain and key out-of-band before publishing anything.
- **Publish:** the operator appends the participant record via `dedid seed` /
  operator tooling. The new identity is live on the next checkpoint, with an
  inclusion proof like any other record.
- **Revoke:** same path, in reverse — a revocation is a new version with a
  revoked state, never a deletion; history stays provable.

Enforcement today is structural: dedid exposes **no write API**. The only
write paths are the operator's CLI and the database, so an unsolicited party
cannot onboard even by trying.

## M2 direction (design sketch, not built)

The publisher plane (design.md §5.3) turns the manual step into an
authenticated workflow without opening the gate:

- **Operator-granted publisher keys**: applicants still apply out-of-band;
  approval mints a scoped publisher credential (namespace/registry-bound),
  recorded in the log.
- **Wildcard eligibility constraint** (binding, design.md Addendum C): only
  allowlisted namespaces/registries participate in Beckn wildcard resolution,
  so a publisher grant is never an implicit grant of network-wide identity
  authority.
- **Multi-party option**: for consortium networks, approval can require k-of-n
  operator signatures (council model) — the log records the quorum, witnesses
  attest the history.

## Non-goals

- Token-based or stake-based governance.
- Anonymous self-registration in any mode.
