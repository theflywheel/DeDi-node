# Quickstart

From nothing to a signed, provable record in about five minutes. You need
Docker, `curl` and `jq`. No Go toolchain and no source checkout: everything
runs from the published image
[`flywheelai/dedi-node`](https://hub.docker.com/r/flywheelai/dedi-node).

Tags are `latest`, `main`, and `sha-<commit>` for an exact build. Pin a
`sha-` tag for anything you care about; `latest` moves.

## 1. Start Postgres and a node

```sh
docker network create dedi-net
docker run -d --name dedi-pg --network dedi-net \
  -e POSTGRES_USER=dedi -e POSTGRES_PASSWORD=dedi -e POSTGRES_DB=dedi \
  postgres:16-alpine
until docker exec dedi-pg pg_isready -h 127.0.0.1 -U dedi -q; do sleep 1; done

docker run -d --name dedi-node --network dedi-net -p 8080:8080 \
  -e DATABASE_URL='postgres://dedi:dedi@dedi-pg:5432/dedi?sslmode=disable' \
  -e DEDI_ORIGIN=localhost/log \
  flywheelai/dedi-node:latest
until curl -sf localhost:8080/healthz >/dev/null; do sleep 1; done
```

`DATABASE_URL` and `DEDI_DB_URL` both work; `DEDI_DB_URL` wins if both are
set. There is no key to generate: on first start the node mints its Ed25519
identity key and keeps it in its own database, so it survives restarts. It
prints the public half on every boot:

```sh
docker logs dedi-node 2>&1 | grep -A1 'verifier key'
```

That verifier key is what anyone checking your checkpoints, or witnessing your
node, needs. `DEDI_ORIGIN` names your log; it is the first line of every
checkpoint, so give a real node its real domain.

## 2. Look at it

```sh
curl -s localhost:8080/healthz | jq .
curl -s localhost:8080/dedi/log/checkpoint
curl -s localhost:8080/dedi/lookup/demo | jq .
```

The checkpoint is a signed note: origin, tree size, Merkle root, signature. The
lookup is a `404` in the standard's error envelope, because nothing has been
published. A fresh node is read-only, and that is the intended starting state:
there are no write routes until you configure a publisher key, so `/admin`
answers 404, not 401.

Open `http://localhost:8080/` in a browser for the node's own pages.

## 3. Open the write plane

Mint a publisher key for one namespace. The private half stays in a file you
keep; the node is only ever given the public half.

```sh
KEYS=$(docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/w" -w /w \
  flywheelai/dedi-node:latest pubkeygen -kid op-1 -namespace demo \
  | sed -n 's/^DEDI_PUBLISHER_KEYS=//p')
echo "$KEYS"
```

It writes `publisher.key` and prints a line `DEDI_PUBLISHER_KEYS=op-1:demo:<base64>`,
which the command above keeps in `$KEYS`. Restart the node with it and an admin
password:

```sh
export DEDI_ADMIN_PASSWORD=change-me
docker rm -f dedi-node
docker run -d --name dedi-node --network dedi-net -p 8080:8080 \
  -e DATABASE_URL='postgres://dedi:dedi@dedi-pg:5432/dedi?sslmode=disable' \
  -e DEDI_ORIGIN=localhost/log \
  -e DEDI_PUBLISHER_KEYS="$KEYS" \
  -e DEDI_ADMIN_PASSWORD="$DEDI_ADMIN_PASSWORD" \
  flywheelai/dedi-node:latest
until curl -sf localhost:8080/healthz >/dev/null; do sleep 1; done
```

The identity key is in the database, so the node comes back as the same node.

Two gates now guard every write, and they answer different questions. The
admin password (HTTP Basic, user `admin` unless `DEDI_ADMIN_USER` says
otherwise) decides who may reach `/admin` at all. The publisher signature
decides who is writing and whether that key may write to this namespace, and it
is what the log records as `created_by: publisher:op-1`. Neither replaces the
other.

Set `DEDI_WILDCARD_NAMESPACES` only if this node serves the Beckn ONIX registry
lookup; see [configuration](configuration.md). Without it the node starts with
that lookup switched off.

## 4. Make a signed write

`dedid sign` prints the headers that authenticate one request. It signs the
method, path, body, timestamp and precondition, so it runs once per request.
This shell function signs a request with the image and sends it:

```sh
send() {  # send METHOD PATH BODYFILE [-create | -if-match TAG]
  docker run --rm -v "$PWD:/w" -w /w flywheelai/dedi-node:latest \
    sign -key publisher.key -kid op-1 -method "$1" -path "$2" -body "$3" "${@:4}" > headers.txt
  curl -s -u "admin:$DEDI_ADMIN_PASSWORD" -X "$1" "localhost:8080$2" \
    -H @headers.txt -H 'Content-Type: application/json' --data-binary @"$3"
}
```

Create a namespace, a registry in it, and a record in that:

```sh
echo '{"payload":{"description":"my first namespace"}}' > ns.json
echo '{"payload":{"description":"people we know"}}'     > reg.json
echo '{"payload":{"name":"Alice","url":"https://alice.example"}}' > alice.json

send PUT  /admin/namespaces/demo                 ns.json  -create | jq -c .data
send PUT  /admin/namespaces/demo/registries/people reg.json -create | jq -c .data
send POST /admin/namespaces/demo/registries/people/records/alice/publish alice.json -create | jq -c .data
```

`-create` means "this must not exist yet" (`If-None-Match: *`). Every write
that replaces something must say which version it replaces, so a replayed or
racing write fails with `412` instead of silently overwriting. To change Alice,
pass the current `version_tag`:

```sh
tag=$(curl -s localhost:8080/dedi/lookup/demo/people/alice | jq -r .data.version_tag)
echo '{"payload":{"name":"Alice","url":"https://alice.example/v2"}}' > alice.json
send POST /admin/namespaces/demo/registries/people/records/alice/publish alice.json -if-match "$tag" | jq -c .data
```

## 5. Read it back, with proof

```sh
curl -s 'localhost:8080/dedi/lookup/demo/people/alice?proof=inclusion' | jq .
curl -s  localhost:8080/dedi/versions/demo/people/alice | jq .data.versions
```

The proof block carries the leaf, its index, the audit path, and the signed
checkpoint it verifies against. `http://localhost:8080/verify` checks it in
your browser with no help from the node. Every earlier version stays
resolvable with `?version_id=<id>` from the versions list.

## Clean up

```sh
docker rm -f dedi-node dedi-pg && docker network rm dedi-net
rm -f publisher.key headers.txt ns.json reg.json alice.json
```

`pubkeygen` refuses to overwrite an existing `publisher.key`, so remove it
before running the quickstart again.

## Next

- [Configuration](configuration.md): every variable and CLI flag.
- [API](api.md): every route and what it accepts.
- [Operator console](operator-console.md): the same writes from a browser, at `/admin`.
- [Deployment modes](deployment-modes.md): getting another node to witness yours.
- [One-click deploy](railway-template.md): the same node on Railway.
