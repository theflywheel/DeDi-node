package refschemas

// Refresh the embedded copy from the vendored spec with:
//
//	go generate ./internal/refschemas/...
//
// go:embed cannot reach outside its package directory (docs/spec/lfdt/schemas
// is two levels up), so this just copies the five reference schemas in
// verbatim. docs/spec/lfdt/schemas remains the authoritative, read-only
// vendored copy — this directive only ever reads from it, never writes to it.
//
//go:generate sh -c "cp ../../docs/spec/lfdt/schemas/Beckn_subscriber.json ../../docs/spec/lfdt/schemas/Beckn_subscriber_reference.json ../../docs/spec/lfdt/schemas/public_key.json ../../docs/spec/lfdt/schemas/membership.json ../../docs/spec/lfdt/schemas/revoke.json schemas/"
