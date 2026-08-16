# Embedded reference schemas

These five files are a verbatim copy of the corresponding files in
`docs/spec/lfdt/schemas/` as of commit `52e120d`, copied in here because
`go:embed` cannot embed a path outside its own package directory.

`docs/spec/lfdt/schemas/` is the authoritative, read-only vendored copy of the
LFDT DeDi spec's reference registry schemas — never edit it, and never edit
the copies in this directory by hand either.

To refresh after the upstream spec changes:

```
go generate ./internal/refschemas/...
```

Files:

- `Beckn_subscriber.json`
- `Beckn_subscriber_reference.json`
- `public_key.json`
- `membership.json`
- `revoke.json`
