# Built-in reference registry schemas

The DeDi standard ships five reference registry schemas, vendored read-only
at `docs/spec/lfdt/schemas/`:

- `Beckn_subscriber.json`
- `Beckn_subscriber_reference.json`
- `public_key.json`
- `membership.json`
- `revoke.json`

Per-registry JSON Schema is enforced on every record write
(`store.ValidateAgainstSchema`, called from the record-publish path in
`internal/api/admin.go`), but the schema a registry enforces is whatever an
operator pastes into the registry's `schema` field when they create it. Two
nodes can each stand up a registry named `public_key` and enforce two
different shapes for it, because nothing ties the name to a canonical
definition — a typo or an out-of-date paste just silently narrows or widens
what a node accepts.

## Referencing a built-in schema

`internal/refschemas` embeds the five schemas above into the `dedid` binary.
When creating a registry (`PUT
/admin/namespaces/{namespace}/registries/{registry}`), set the payload's
`schema` field to a string of the form `builtin:<name>` instead of pasting a
schema object:

```
PUT /admin/namespaces/ns/registries/keys
{
  "payload": { "schema": "builtin:public_key" }
}
```

The built-in names are the spec filenames above minus `.json`:
`Beckn_subscriber`, `Beckn_subscriber_reference`, `public_key`, `membership`,
`revoke`.

The node resolves the reference before the registry is stored
(`resolveBuiltinSchema` in `internal/api/admin.go`) and writes the full,
resolved schema object into the registry's payload — the stored registry, and
everything downstream of it (lookups, versions, the record-write schema
check), is identical to a registry where the schema was pasted by hand. A
`schema` that is already an object, absent, or a plain string not prefixed
`builtin:` is left untouched. An unrecognized `builtin:<name>` is rejected
with `400 INVALID_REQUEST` at creation time rather than being stored
unresolved.

This only standardizes the five reference shapes; an operator with a
domain-specific registry still pastes its schema as before.

## Refreshing the embedded copies

`go:embed` cannot reach outside its own package directory, so
`internal/refschemas/schemas/` holds a copy of the five files, not a
reference to `docs/spec/lfdt/schemas/`. If the vendored spec is updated,
refresh the copy with:

```
go generate ./internal/refschemas/...
```

See `internal/refschemas/schemas/README.md` for the copy's provenance.
