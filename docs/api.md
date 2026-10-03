# API

Every route the node serves, what it accepts, and what it answers. The eight
`/dedi/lookup|query|versions` routes are the DeDi standard's read API
([`api/openapi.yaml`](https://github.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/blob/52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18/api/openapi.yaml));
everything else is this node's extension, and [conformance](conformance.md)
says which is which.

Responses use the standard's envelope: `{"message", "data"}` on success, with
a `proof` beside `data` when one was asked for, and
`{"message", "error", "code"}` on failure.

## Read routes

| Route | Query parameters | Returns |
|---|---|---|
| `GET /dedi/lookup/{ns}` | `version_id`, `as_on`, `proof`, `include_revoked`, `internal` | a namespace |
| `GET /dedi/lookup/{ns}/{reg}` | same | a registry, with its schema |
| `GET /dedi/lookup/{ns}/{reg}/{rec}` | same | a record; its payload is `data.details` |
| `GET /dedi/query/{ns}` | `name`, `status`, `state`, `from`, `to`, `as_on`, `sort`, `page`, `page_size`, `internal` | a page of the namespace's registries |
| `GET /dedi/query/{ns}/{reg}` | same; **or** `domain` (with only `internal`) | a page of the registry's records; with `?domain=`, who serves that domain ([discovery](discovery.md)) |
| `GET /dedi/versions/{ns}[/{reg}[/{rec}]]` | `internal` | every version id, oldest first |
| `GET /dedi/log/checkpoint` | — | the latest signed checkpoint, `text/plain` |
| `GET /dedi/log/proof/consistency` | `old`, `new` (`1 ≤ old ≤ new ≤` tree size) | an RFC 6962 consistency proof |
| `GET /dedi/log/history` | `buckets` (48), `bucket_seconds` (1800, min 60) | checkpoints and writes over time; what `/status` draws |
| `GET /dedi/witness` | — | every verdict this node holds about other logs ([witnessing](witnessing.md)) |
| `GET /dedi/witness/{origin}` | — | one target's verdict, with the inclusion proof of the verdict itself; `{origin}` path-escaped |
| `GET /dedi/delegations/{ns}` | — | children delegated under `{ns}`, with witness verdict and loop health ([delegation](delegation.md)) |
| `GET /dedi/network` | — | peers, reachability, witness health, cluster state |
| `GET /dedi/stats` | — | requests served by status class, uptime |
| `GET /.well-known/dedi.index.json` | — | the signed DeDi manifest ([file publication](file-publication.md)) |
| `GET /dedi-files/{ns}/dedi.{reg}.json` | — | one signed DeDi file |
| `GET /healthz` | — | flat JSON: database reachability, tree size, checkpoint age, cluster role; `503` only if the database is unreachable |
| `GET /metrics` | — | Prometheus text: replica lag, peer reachability |

HTML pages: `/` (overview), `/browse`, `/network`, `/status`, `/check`,
`/verify`, `/docs`, `/docs/{page}`, and `/admin` when the write plane is open.

### Strict query parameters

The lookup, query and versions routes refuse a query they would otherwise
answer by quietly ignoring part of it. Each of these is a `400`:

- a key the route does not read (`?versionId=2`, `?asOn=…`);
- a key given twice (`?version_id=&version_id=2`);
- a query string that does not parse (a stray `;`, a bad `%` escape);
- on `?domain=` discovery, any key other than `domain` and `internal`.

Why: `?versionId=2` used to return the latest version, with a valid proof
attached. Every check a careful client ran passed, because they were all about
the record the node chose to return rather than the one asked for.

### Parameter meanings

- **`version_id`** pins one version. It is the log sequence number, the strings
  `/dedi/versions` lists and lookup returns as `data.version`. One that names
  no version of this resource, including a non-numeric one, is a `404`; the
  standard types it as an unconstrained string, so it is not a malformed
  request.
- **`as_on`** answers "as of this instant": an RFC 3339 timestamp, or a
  `YYYY-MM-DD` date read as the end of that day in UTC. `from` takes the start
  of the day, `to` the end. Anything else is a `400`.
- **`proof=inclusion`** attaches `proof`: the leaf, its index, the audit path,
  and the signed checkpoint it verifies against. Any other value is a `400`.
- **`include_revoked=true`** lets a record lookup return a revoked current
  version (`state: revoked`) instead of `404`.
- **`internal=1`** makes `_`-prefixed bookkeeping namespaces (`_witness`,
  `_domains`, `_crawl`, …) visible; without it they `404`, so a crawler
  reading the standard's endpoints does not index them as directory data.
- **`sort`** is one of `date`, `status`, `name`, `id` (`id` sorts by name).
  `page` and `page_size` are positive integers; `page_size` defaults to 25 and
  is capped at 100.

### Now, or settled?

A lookup is a **settled** read when it pins a `version_id`, or asks for an
`as_on` more than one minute in the past. Its answer cannot change, so it is
served `Cache-Control: immutable` for a year, and a revoked record still
answers (history stays readable).

Anything else is a **now** read: no `as_on`, an `as_on` of today (end of day),
in the future, or under a minute ago. It is cached for the record's `ttl` only,
because that is how a revocation reaches a consumer, and a revoked record
answers `404` unless `include_revoked=true`. The one-minute margin covers a
write whose timestamp was stamped just before it committed.

Every lookup carries an `ETag`; send it back as `If-None-Match` for a `304`.

## Versions, three numbers

| Field | Is | Used for |
|---|---|---|
| `version_id` (also `version`) | the log sequence number of that version | `?version_id=` |
| `version_num` | 1, 2, 3… within this one resource | reading, and the Merkle leaf |
| `version_tag` | `<sha256 of the payload>-<state>` | `If-Match` on the next write |

A write's response carries all three, so the next request never has to
reconstruct one.

## Write routes

Registered only when `DEDI_PUBLISHER_KEYS` is set; otherwise they, and
`/admin`, answer `404`. Every one needs a publisher signature
(`DeDi-Key-Id`, `DeDi-Timestamp`, `DeDi-Signature`, from `dedid sign` or the
console) by a key scoped to `{ns}`, and the Basic-auth gate when
`DEDI_ADMIN_PASSWORD` is set. Bodies are JSON.

| Route | Body | Precondition |
|---|---|---|
| `PUT /admin/namespaces/{ns}` | `{"payload": {…}}` | required |
| `PUT /admin/namespaces/{ns}/registries/{reg}` | `{"payload": {…}}`; `"schema": "builtin:<name>"` picks a [reference schema](reference-schemas.md) | required |
| `POST …/registries/{reg}/records/{rec}/publish` | `{"payload": {…}}`, validated against the registry's schema | required |
| `POST …/registries/{reg}/records/{rec}/revoke` | optional `{"reason": "…"}` | `If-Match` |
| `POST …/registries/{reg}/subscriptions` | `{"target_url": "https://…"}` | none |
| `GET /admin/namespaces/{ns}/subscriptions` | — | none |
| `DELETE /admin/namespaces/{ns}/subscriptions/{id}` | — | none |
| `POST /admin/namespaces/{ns}/children` | `{"namespace": "{ns}.child", "role": "child", …}` | none |
| `POST /admin/namespaces/{ns}/children/{child}/revoke` | — | none |
| `POST /admin/node-config` | role and its inputs | none |
| `GET /admin/namespaces/{ns}/domain` | — | none |
| `POST /admin/namespaces/{ns}/domain/verify` | — | none |
| `DELETE /admin/namespaces/{ns}/domain` | — | none |

"Required" means `If-None-Match: *` (must not exist yet) or
`If-Match: <version_tag>` (replace exactly this version). Both headers are
inside the signature, so a captured request cannot be replayed without its
precondition, and a replay lands as a `412` once the version has moved on.
Publishing a record identical to its current version, or revoking one that is
already revoked, is a no-op `200` with `"unchanged": true` rather than a
duplicate version.

`POST /enrol` is the one write that is not signed: a child presents its
one-time token there ([delegation](delegation.md)). `POST /dedi/enrol` is a
deprecated alias.

## Errors

One mapper handles every write failure, so the same cause gets the same answer
on every route.

| Status | `code` | When |
|---|---|---|
| 400 | `INVALID_REQUEST` | bad query (above), malformed body, a payload the schema rejects, malformed signature headers |
| 401 | `UNAUTHORIZED` | unsigned, unknown key id, bad signature, timestamp more than 5 minutes off; or the Basic-auth gate |
| 403 | `FORBIDDEN` | the key is scoped to another namespace |
| 404 | `NOT_FOUND` | no such resource or version; a hidden `_` namespace; a write under a namespace or registry that does not exist (the message names it) |
| 409 | `CONFLICT` | a delegation state that will not change by retrying: already delegated, offer already redeemed |
| 412 | `PRECONDITION_FAILED` | the `If-Match` / `If-None-Match` did not hold; the message carries the current `version_tag` |
| 428 | `PRECONDITION_REQUIRED` | a write that needs a precondition sent none |
| 500 | `INTERNAL` | a fault on the node |

On a [replicated](replication.md) node only the leader writes. **Every** write
that reaches a follower, domain verification included, is answered
`307 Temporary Redirect` to the same path on the leader, method and body
intact, so a client that follows redirects needs no cluster awareness. When
there is no leader, or the leader's public URL is not configured in
`DEDI_CLUSTER_PEERS`, the follower answers `503` with code `NO_LEADER` and
`Retry-After: 2`. Reads are served by every replica.
