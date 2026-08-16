package refschemas

// Refresh the embedded copy from the spec submodule with:
//
//	go generate ./internal/refschemas/...
//
// go:embed cannot reach outside its package directory (docs/spec/lfdt/schemas
// is two levels up), so this just copies the five reference schemas in
// verbatim. docs/spec/lfdt is the pinned spec submodule and is authoritative
// and read-only — this directive only ever reads from it, never writes to it.
//
// It therefore needs the submodule checked out (`git submodule update --init`)
// and it names the five files by exact case. Upstream has renamed these before
// — if the pin moves and cp reports a missing file, the fix is to update the
// names here, not to drop the file from the list.
//
//go:generate sh -c "cp ../../docs/spec/lfdt/schemas/Beckn_subscriber.json ../../docs/spec/lfdt/schemas/Beckn_subscriber_reference.json ../../docs/spec/lfdt/schemas/public_key.json ../../docs/spec/lfdt/schemas/membership.json ../../docs/spec/lfdt/schemas/revoke.json schemas/"
