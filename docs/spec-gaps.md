# Open gaps against the DeDi standard

Companion to [`conformance.md`](conformance.md), which records what we *do*
implement. This file records what we do not, and what we implement wrongly.

Measured against the vendored spec at [`spec/lfdt/`](spec/lfdt/), commit
`52e120d`. Every claim here was checked by running code or reading the spec
text, never inferred from our own docs — the entries marked **proven** carry the
test that demonstrates them.

Ordered by severity, which is also the order they should be fixed.

**Status.** G1, G2, G4, G5, G6 and G8 are fixed — see each entry. G3 and G7
remain open, and the first of those is a pull request rather than code. Every
fix was re-verified by running it, and the full suite passes twice in a row
under `make test`.

---

## G1 — File output varies with the wall clock — **fixed** (task #50)

> Resolved by quantizing `next_update` to the freshness window containing the
> build time, and deriving every `updated_at` from the newest log entry the file
> covers. Two builds inside one window are byte-identical, so the digest holds,
> the ETag stops churning, and a conditional re-fetch returns 304 — all three
> re-verified over HTTP with the same 1.2s gap that exposed the bug.
> `TestOutputIsStableAcrossTheFreshnessWindow` pins it.

The original finding, kept because it is the reason G2 matters:


`dedifile.Build` runs on every request with `Now = time.Now()`, so `next_update`
is recomputed each time and the file's bytes change with it.

The manifest commits to `sha-256:` of each file it lists. Because the manifest
and the file are built by two separate requests, that digest is computed against
bytes that no longer exist by the time a crawler fetches the file:

```
manifest committed digest : sha-256:78660f8dfa383f17f144867ff3f2edb4b42cf2df29e98320c360f8a1126107f8
served file actual digest : sha-256:b2c852abb046808a76c661d7040ea228e6faeb585398ea67bc7aab2036436aa2
```

— one manifest fetch, a 1.2 second pause, one file fetch. Spec §6.3 makes that
digest how a signed manifest vouches for a file *before* fetching it, and step 3
of §7.3 is where a verifier checks it. A conformant crawler either rejects our
file or re-fetches in a loop.

Two further symptoms of the same cause:

- **ETag churn.** The digest we derive the ETag from changes every second, so
  the conditional-fetch path we built never returns 304 and every crawler
  re-downloads everything on every poll.
- **`updated_at` is noise.** §9 draws a sharp line: `next_update` advances on
  every re-issue, `updated_at` advances *only when content actually changes* —
  that difference is exactly how a re-published revocation list signals "nothing
  new." §12 has monitors watching `updated_at` for regressions as a rollback
  defence. Ours moves on every build, so both signals are destroyed.

And the inverse, on real registries: `updated_at` is taken from the registry
entry's own timestamp (`build.go`), so publishing or revoking a *record* changes
the file's content without advancing `updated_at`. Content changes silently —
the precise failure §12's monitors exist to catch.

**Fix.** Derive both timestamps from the log rather than the clock: `updated_at`
from the newest content that went into the file, `next_update` from that same
basis so it is stable between real changes. A build keyed to the log's head is
then byte-identical until the log moves, which fixes the digest, the ETag, and
both `updated_at` bugs at once.

---

## G2 — Tests skip silently without a database — **fixed** (task #51)

> `internal/testdb.URL` is now the single place that decides what "no database"
> means, and it fails. Verified both ways: `go test ./internal/api/ ./conformance/`
> with no `TEST_DATABASE_URL` now FAILs with instructions, and the same command
> with `-short` skips and passes. With a database, `internal/api` skips 0 of 122.
>
> The second half — per-package schemas so the suite is honest under parallel
> `./...` — was **not** done. `make test` already pins `-p 1` for exactly this
> reason, and that is the supported way to run it; isolation would only buy
> parallelism, not correctness.

The original finding:


Without `TEST_DATABASE_URL`, `testStore`/`testServer` call `t.Skip`. `go test`
still prints `ok`:

| Package | Without the variable | With it |
|---|---|---|
| `internal/api` | 108 skipped, 14 run | 0 skipped |
| `conformance` | 3 of 4 skipped — every spec assertion | all run |

A green suite that has verified nothing is worse than a red one. This is how G1
shipped: the schema-validation and round-trip tests that would have caught a
drifting digest were never executed in the run that declared the work done.

**Fix.** Fail rather than skip when the variable is unset and the run was not
explicitly opted out (`-short`, or an explicit env flag), so "no database" is a
loud result. Separately, the packages share one database and clobber each other
under parallel `./...`; give each its own schema so the suite is honest at
`-p` > 1 too.

---

## G3 — Not discoverable: absent from any `domains.txt`

§13's publisher clause requires being "discoverable via the list and/or crawl."
We satisfy every other publisher condition and fail this one: our domain appears
on no list. §11 keeps the root `domains.txt` at the root of the LFDT protocol
repository.

The list is explicitly not a trust anchor — "any party may add any domain, and
doing so confers no authority" — so this is a pull request, not code.

---

## G4 — Canonical schema URLs point at a mutable branch — **fixed** (task #52)

> `refschemas.URL(name)` now pins to `SpecCommit` — the same commit the embedded
> copies were generated from, so what we advertise and what we enforce cannot
> drift. Published files carry
> `.../52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/schemas/revoke.json`, confirmed
> over HTTP as a live 200. Two tests guard it: one rejects any `/main/` URL,
> one fails if `SpecCommit` stops matching the commit `docs/spec/README.md`
> records as vendored.

The original finding:


§10 carries an explicit warning: the canonical schema URLs point at `main`,
which is mutable, so "editing a schema on `main` would silently change the
meaning of every DeDi file that references it," and they **MUST** be pinned to
an immutable ref before release.

Our revocations registry references `.../main/schemas/revoke.json`, inheriting
the problem. `internal/refschemas` already vendors from pinned commit `52e120d`
while advertising the unpinned URL, so the fix is to advertise what we actually
vendored.

---

## G5 — No `Cache-Control` bound — **fixed** (task #53)

> `cacheControlUntil` derives `max-age` from the artifact's own `next_update`,
> so the HTTP layer never promises more than the document does. An unparseable
> or already-past `next_update` yields no header at all rather than a zero or
> negative age, leaving revalidation to the ETag. Two tests: the unit boundaries,
> and an end-to-end one asserting the served header cannot outlive the
> `next_update` in the body it arrived with.

The original finding:


§5.2: a publisher SHOULD NOT advertise a `max-age` extending beyond the file's
`next_update`, because "an HTTP cache outliving that bound would serve copies
the protocol has already declared stale."

We set no `max-age` at all, which does not violate the SHOULD NOT — but it
leaves caching to intermediary defaults. Once G1 makes the bytes stable, a
`max-age` derived from `next_update` is the correct posture. Fix after G1, not
before: caching output that changes every second would be actively harmful.

---

## G6 — `version_id` is typed `string` but we reject non-numeric — **fixed** (task #54)

> Loosened, as the safer direction against a spec we do not control: an
> unrecognizable `version_id` is now 404 ("no such version") rather than 400
> ("your request is malformed"). `as_on` deliberately still answers 400, because
> the spec gives it `format: date-time` and a bad value there really does
> violate the contract. `conformance/`'s logging test is now a real assertion,
> so a regression to 400 fails the suite.

The original finding:


Found by `conformance/`, not by three careful readings of the same YAML.
`openapi.yaml` types the lookup `version_id` parameter as an unconstrained
`string`; our handler answers 400 — "version_id must be an integer version id" —
for anything non-numeric. A client sending a value the documented type permits
is rejected.

Resolution is a product call, which is why the suite logs it rather than
asserting either answer: loosen our handler (404 for an unparseable id, treating
it as "no such version" rather than a client error) or file the tightening
upstream. Loosening is the safer default against a spec we do not control.

---

## G7 — No crawler: we are a publisher, not yet a server

§13's **DeDi server** clause has four conditions. We meet one:

| Condition | Us |
|---|---|
| verifies every ingested file end-to-end, rejects unauthenticated data | ✗ — ingests nothing |
| serves publishers' records and signatures unaltered | ✗ |
| exposes every ingested record at `{namespace}/{registry}/{record}` | ✗ |
| honors freshness and registry state | ✓ |

§1.3 makes "DeDi server" a distinct, optional role rather than a level of
publisher conformance, so this is a deliberate scope decision, not an accident.
It also has no present utility: nobody else publishes DeDi files yet, so a
crawler would crawl an empty world. Worth building when the ecosystem exists, or
sooner if conformance as a *server* becomes a goal.

---

## G8 — Namespace authority is asserted, not proven — **fixed** (task #56)

> `internal/domainproof` derives a per-(namespace, domain, node) TXT challenge
> and checks it live; `internal/api/domain.go` records the verdict as a log
> entry in the `_domains` bookkeeping namespace, so it replicates through Raft,
> lands in the Merkle tree, and can be withdrawn by appending rather than by
> rewriting. Namespace and domain stay separate identifiers — only the edge
> between them became checkable.
>
> Two design points worth keeping: the token is **derived, not issued**, so
> there is no pending-challenge table to expire or lose in a failover; and it is
> **non-transferable**, so a TXT record proving one namespace proves nothing for
> another namespace, another domain, or another node. Both are asserted by
> tests, the second in two places.
>
> The verdict is deliberately **not** on `/dedi/lookup`. The standard's response
> schema declares no field to carry it, and adding one would trade a closed gap
> for a new out-of-spec extension; it lives on the write plane instead, and
> `TestDomainBookkeepingStaysOffTheSpecSurface` fails if that changes.

The original finding:



Our `publisher.domain` is the node's origin while `namespace` is an independent
identifier — deliberately, since a namespace may name a sector or community
while the domain is merely where it is served. The spec permits this: the schema
says a namespace is "usually the domain," never that it must be.

What is missing is the binding. Nothing proves this node may speak for a
namespace whose domain it does not serve from. dedi.global solves the same
problem with DNS TXT verification
(`generate-dns-txt` → operator publishes the record → `verify-domain`), which
keeps namespace and domain distinct while making the link checkable, and needs
no crawler.

We already store `domain` on namespaces and query by it (`?domain=`), so the
work is the verification step and a verified flag, not a data model change.
