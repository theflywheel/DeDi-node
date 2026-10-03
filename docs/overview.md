# Overview

DeDi Node is a self-hostable, open-source server for the Decentralized
Directory protocol, with an append-only Merkle log underneath so anyone can
check that what it serves was published and never rewritten.

## What DeDi is

In the standard's own words, DeDi is "an open protocol for publishing public
directories — public key directories, revocation and sanctions lists,
membership rolls, professional and company registers — so that any party can
discover them and verify what they say." Data is organised as **namespaces**
(an organisation, usually a domain), **registries** (lists of records with a
schema) and **records**. The standard is maintained by LF Decentralized Trust:
[decentralized-directory-protocol](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/tree/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18),
which this node is measured against at commit `52e120d`.

It defines two things: a read API (lookup, query, versions) and a way to
publish directories as signed files. It does not define how records get
written, and it does not keep history tamper-evident. This node adds both.

## What this node implements

**The standard**

- All 8 read endpoints of the DeDi API ([API](api.md), [conformance](conformance.md)).
- Signed DeDi files and the `/.well-known/dedi.index.json` manifest ([file publication](file-publication.md)).
- A crawler that verifies and serves other publishers' files ([crawler and mirror](crawl-mirror.md)).
- The five reference registry schemas, built in ([reference schemas](reference-schemas.md)).

**Extensions**

- **A signed write plane.** 13 `/admin` routes; every write is Ed25519-signed by
  a publisher key scoped to one namespace; namespace, registry and record writes
  also carry a mandatory precondition so a
  replay or a race fails instead of overwriting ([operator console](operator-console.md)).
- **A Merkle log.** Every version is a leaf of an RFC 6962 tree; the node signs
  C2SP checkpoints, and any lookup can carry an inclusion proof. Consistency
  proofs show the log only ever grew ([architecture](architecture.md)).
- **Witnessing.** Any node can check another's log is append-only and publish
  the verdict ([witnessing](witnessing.md)).
- **Raft replication** for availability ([replication](replication.md)).
- **Child-namespace delegation** to a node someone else runs, witnessed by the parent ([delegation](delegation.md)).
- **Webhooks**: signed push when a record changes ([push](push.md)).
- **DNS domain binding**: prove a namespace's domain with a TXT record.
- **Discovery**: who serves a domain ([discovery](discovery.md)).
- **CORD anchoring** of checkpoints, optional ([deployment modes](deployment-modes.md)).
- **Pages**: an overview at `/`, a directory browser at `/browse`, the witness
  ring at `/network`, signing history at `/status`, record and proof checkers
  at `/check` and `/verify`, the operator console at `/admin`, and these docs.

It is a Merkle log, not a blockchain. There is one writer per log, no
consensus among strangers and no token. What stops an operator rewriting
history is that the rewrite cannot be made consistent with a checkpoint someone
else already holds, and witnesses are the someone else.

## Five roles

One binary, configured by environment variables, plays any of these. A node
can also witness another whatever its role.

| Role | What it is |
|---|---|
| **standalone** | a directory of its own: its own key, its own log. What a first node is. |
| **mirror** | serves what it crawled from other publishers; accepts no writes. |
| **witness** | checks another node's log is append-only and records verdicts. |
| **replica** | one member of a Raft set sharing one identity. Uptime, not trust. |
| **child** | holds one namespace delegated by a parent node, under its own key. |

[Deployment modes](deployment-modes.md) explains how roles and trust levels
combine; the console's **Add a node** tab renders the configuration for each.

## Where to go next

- Run one: [quickstart](quickstart.md), then [configuration](configuration.md).
- Use one: [API](api.md).
- Understand why: [why](why.md), [architecture](architecture.md).
- See it used: [a Beckn network](beckn-demo.md), [CREST](crest.md).
- A public node to try: **https://dedi.beckn.try-dough.com**.
- The conformance suite, runnable against any DeDi server:
  [theflywheel/dedi-conformance](https://github.com/theflywheel/dedi-conformance).
