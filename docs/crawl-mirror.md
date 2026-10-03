# Crawler and mirror

The DeDi standard has two roles: a **publisher** signs files and hosts them, and
a **server** crawls publishers and answers the read API for what it found. This
node is a publisher by default ([file publication](file-publication.md)). With
`DEDI_CRAWL_DOMAINS` set it is also a server for other people's directories.
A **mirror** is a node that does only that.

## Turning it on

```sh
DEDI_CRAWL_DOMAINS=registry.example.org,other.example   # who to crawl
DEDI_CRAWL_INTERVAL=1h                                  # default
```

Domains are fetched over `https://` unless written with an explicit `http://`.
Every domain is crawled at start and then on the interval. One domain failing
never stops the others; each failure is logged as `crawl <domain>: <error>`.

Off unless set, on purpose: crawling is a "make this server fetch a URL"
capability. For the same reason a domain that resolves to a private, loopback
or link-local address is refused unless `DEDI_ALLOW_PRIVATE_WEBHOOK_TARGETS=true`
(the name is historical; this switch governs the crawler, not webhooks).

## What a crawl checks

For each domain, every check below runs before anything is ingested:

1. fetch `/.well-known/dedi.index.json` and verify the manifest's own JWS;
2. refuse it if `next_update` is in the past;
3. for each file it lists: the URL must be on the same origin, the bytes must
   match the digest the manifest committed to, the file's own JWS must verify,
   and record names must be unique;
4. **key pinning**: the first successful crawl pins the key the domain signs
   with, in this node's `_crawl/sources` registry. A later crawl signed by a
   different key stops with an error and that source is marked `revoked`,
   rather than ingesting whatever the new key vouches for. A manifest is
   self-signed, so its signature proves only internal consistency; continuity
   with the last key is what catches a takeover. The node has no command to
   accept a new key: the domain stays stopped until the pin is changed.

Verification is all-or-nothing, because a partial crawl would leave a copy that
disagrees with the manifest that vouched for it. Ingestion itself is not: files
are appended one at a time, so if a later file is refused at ingest time (for
example, it names a namespace this node owns), the files before it have already
been written.

## What gets stored

Each crawled file lands in this node's log, so its records answer at the
standard's paths, `/dedi/lookup/{namespace}/{registry}/{record}`, the same way
as anything published here:

- the namespace is created with `mirror_of: <domain>`, and every entry is
  written `created_by: crawler:<domain>`, so provenance is in the log itself;
- the publisher's signed envelope is kept with the registry, so a reader can
  re-verify against the publisher's key rather than trusting this copy;
- an unchanged re-crawl appends nothing, so the log does not grow at the crawl
  rate;
- a record that disappears from a file is revoked here, not deleted, so its
  history says when it stopped being published;
- a crawl may not write into a namespace this node publishes itself, one
  mirrored from a different domain, or any `_` namespace;
- mirrored namespaces are **not** re-published in this node's own DeDi files,
  so crawled data is never re-signed under this node's key and passed off as
  its own.

In a cluster only the leader crawls; replicas receive the result through Raft.

## The mirror role

A mirror is a node with crawl domains and no publisher keys. With no keys its
write plane is not routed at all (`/admin` answers 404), so everything it
serves came from a crawl. It buys reach and a nearby copy, and what it serves
is still checkable two ways: against the original publisher's signature, kept
with the data, and against the mirror's own checkpoints, which cover every
version it ingested.

It cannot be more current than its last crawl. Pick `DEDI_CRAWL_INTERVAL` from
how stale the readers can tolerate, remembering that each publisher's
`next_update` is also a promise about how often its files change.

The console's **Add a node** tab renders a mirror's configuration
(`DEDI_CRAWL_DOMAINS` and no `DEDI_PUBLISHER_KEYS`). Witnessing composes with
it like any other role.

## Known gaps

- Crawler health (last attempt, last error per domain) is kept in memory and
  logged; it is not yet on `/dedi/network` or `/status`. The durable state, the
  pinned key per domain, is readable at
  `/dedi/query/_crawl/sources?internal=1`.
- There is no `domains.txt` support: the node crawls the domains it is given,
  and does not publish itself to a discovery list.
