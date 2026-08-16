# deploy/beckn — the Beckn network this registry serves

A DeDi registry is only interesting if something resolves identities against it.
This is that something: a two-adapter Beckn network whose participants verify
each other's signatures against the node next door.

```
sandbox-bap ──/bap/caller──→ onix-bap ──signs, resolves BPP in DeDi──→ onix-bpp
     ↑                                                                    │
     └──/api/bap-webhook──── onix-bap ←──on_discover, signed──── onix-bpp ─┤
                                                                           ↓
                                                            flywheel-bpp /api/webhook
                                                                           ↓
                                            schemes.proto.theflywheel.in/beckn/search
```

Every arrow between the adapters is a signed Beckn message whose key is looked
up in `dedid` — `GET /dedi/lookup/{subscriber_id}/subscribers.beckn.one/{key_id}`,
which resolves because `DEDI_WILDCARD_NAMESPACES=beckn-testnet`.

## Why flywheel-bpp exists

The schemes corpus, the weather/mandi/news providers and the AI reranker all
live in the flywheel demo. Rather than copy a dataset we do not have, that demo
is registered on this network as a **provider** and `flywheel-bpp` fronts it.

It cannot be cut out. The demo's own Beckn entry point acks synchronously and
then posts its `on_discover` to *its* `BPP_CALLER_URL` — an env var baked to the
beckn-router on its host. An adapter pointed straight at it gets an ack and
never a callback. `flywheel-bpp` receives on our side and posts the callback to
our caller. It builds no catalog of its own: the demo's `GET /beckn/search`
returns the finished `on_discover` envelope, so the body our BAP sees is the
demo's, lifted whole.

`live_check_test.go` (`LIVE=1 go test ./...`) checks that contract against the
real upstream for all four domains.

## Services

| service | source | listens | notes |
|---|---|---|---|
| `onix-bap` | the patched beckn-onix fork, `CONFIG_FILE=/app/config/bap.yaml` | 8081 | signs outbound, verifies callbacks |
| `onix-bpp` | same image, `CONFIG_FILE=/app/config/bpp.yaml` | 8082 | verifies inbound, signs `on_discover` |
| `flywheel-bpp` | `flywheel-bpp/` here | 3002 | the BPP application; fronts the upstream |
| `sandbox-bap` | `sandbox-bap/` here | 8080 | the BAP application: `/trigger`, `/inbox` |
| `redis` | `redis:7-alpine` | 6379 | adapter cache and payload store, db 0 (bap) / 1 (bpp) |

The adapters are not in this repository: they are a fork of `beckn/beckn-onix`
carrying three demo patches (locked-constant env override, a Redis logical db
index, and a Railway image with the config baked in — Railway has no bind
mounts). The fork is where those live; the reason each one exists is in its
commit message.

## Identities

`bap.example.com` and `bpp.example.com`, in `beckn-testnet/subscribers.beckn.one`
on `dedid`, with the record names being the key ids the adapters send. Their
signing keys are in the adapters' config; the public halves are in the registry.
The upstream demo is registered alongside them as a provider record.
