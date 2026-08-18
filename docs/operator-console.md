# The operator console

The write plane's user interface, at `/admin`. Four sections, each producing
signed entries in the node's log — except the last, which only reads.

It used to be seven tabs, and that was the wrong count. Onboarding, key rotation
and revocation are not three tasks: they are one participant at three points in
its life, and the console already knew it — the participants table offered
"rotate key" and "revoke" on every row, and clicking one threw away the
selection, switched tab, and asked you to retype the record name the table had
just shown you.

| Section | What it is | Page |
|---|---|---|
| Participants | the table, and everything you do to a participant: onboard, rotate, revoke | [onboarding](/docs/onboarding), [key rotation](/docs/key-rotation), [revocation](/docs/revocation) |
| Child nodes | a namespace delegated to a node someone else runs | [delegation](/docs/delegation) |
| Push | consumers notified when a record changes | [push](/docs/push) |
| Who serves | a read, not a write: who serves a domain | [discovery](/docs/discovery) |

Rotate and revoke now open against the participant already selected, showing
its name rather than asking for it, and only one opens at a time — they are two
things to do to the same record, and offering both invites filling in one and
submitting the other.

**"Who serves" searches every namespace this node will answer for**, not only
the one selected, and narrows by registry name. That is what discovery means: a
caller asking who serves a domain has no namespace in mind. The console shows
the namespace each result came from, because for a while it did not, and a
cross-namespace answer read as a same-namespace one.

## It holds no privilege of its own

The console is a static page. It is not an admin application with a session that
can write to the log, and there is no server-side "logged in as operator" state
that it drives.

Every write it makes is an **Ed25519 signature computed in your browser** with a
publisher key you paste in, over the method, path and body. The node verifies
that signature against `DEDI_PUBLISHER_KEYS` and accepts or refuses on that
basis alone. Serving the page to someone grants them nothing.

This is why the page can be public on the demo node without the demo being open
to writes, and why the security question for the console is never "who can
reach /admin" but always "who holds a publisher key".

## What happens to the key you paste

It is imported with WebCrypto as **non-extractable**, lives in one JavaScript
closure, and is written nowhere else:

```js
let SIGNER = null;   // {kid, key: CryptoKey}
...
$('k-priv').value = '';   // do not leave it sitting in the DOM
```

Not `localStorage`, not a cookie, never in a request body. The textarea is
cleared the moment the key is imported, so it does not survive in the DOM for a
screenshot or a devtools scroll. Closing the tab is what "logging out" means,
and `Clear key` does the same thing without closing anything.

One practical wrinkle worth knowing: WebCrypto imports Ed25519 private keys as
PKCS#8 only, while `dedid pubkeygen` writes the raw 64-byte Go key
(seed ‖ public). The console wraps the seed in the fixed PKCS#8 prefix for you,
so paste what the CLI gave you and do not convert anything.

## Two independent gates

They are separate, and both apply:

**The publisher key** authorises the *write*. Without a valid signature the node
refuses, whatever else is true.

**The operator gate** (`DEDI_ADMIN_USER` / `DEDI_ADMIN_PASSWORD`) controls who
can *see* the console. It is HTTP basic auth in front of `/admin`, and it
protects against nothing cryptographic — it keeps the page off the open web.

A node with a publisher key and no gate serves the console to anyone and still
accepts no writes from them. A node with a gate and no publisher key has no
write plane at all.

Which brings up the behaviour that surprises people most:

> On a node with no `DEDI_PUBLISHER_KEYS`, `/admin` is **not routed at all**. It
> answers **404**, not 401.

That is deliberate. A 401 advertises that a write plane exists and invites
someone to go looking for the credential. A one-click node is read-only by
design, and a read-only node should not advertise a door it does not have. See
[one-click deploy](/docs/railway-template) for how to open it.

## The participants section

The default view: every record in the namespace, with its state, current version
and the actions available on it.

It reads with `include_revoked=true`, unlike the public read plane. A revoked
participant is exactly what an operator needs to see — to confirm a revocation
landed, to read why, or to bring someone back — and a console that hid them
would be hiding the outcome of its own most consequential action.

Every row links to the record's history, because in a transparency log the
history *is* the record. A participant's current key matters less than the
sequence of keys it has had and when each stopped being valid.

## Everything here is a log entry

There is no separate configuration store, no "settings" that live outside the
log, and no operation that quietly edits state. Onboarding a participant,
rotating a key, revoking someone, delegating a namespace, subscribing a webhook
— each is an append, each is signed, each is permanent, and each carries its
`created_by`.

That is the point of [governance](/docs/governance): the record of who was
admitted and who was removed is not a side effect of administration, it is the
product. An operator who wants to know what changed last Tuesday reads the log,
and so can anybody else.

Nothing is ever deleted. "Delete" is not an operation this node has. Withdrawal
is [revocation](/docs/revocation) — a new entry saying the previous binding no
longer holds — which is why a revoked record stays resolvable and stays
distinguishable from one that never existed.

## Operating it from a script instead

The console signs requests; it is not the only thing that can. `dedid sign`
produces the same signature from a shell, which is what the demo's seeding
script uses:

```sh
dedid sign -key publisher.key -kid op-1 \
  -method POST -path /admin/namespaces/$NS/registries/$REG/records/$NAME/publish \
  -body party.json -curl
```

Anything the console can do, a script can do, with the same key and the same
verification on the node. Use the console to look and to make one-off changes;
use a script when the change should be reviewable and repeatable.
