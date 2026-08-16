// Package conformance measures a running DeDi read-plane handler against the
// DeDi standard at docs/spec/lfdt/api/openapi.yaml, which is a pinned git
// submodule of the upstream standard repository — so these tests need
// `git submodule update --init` and will not build a spec of their own.
//
// The spec file is the source of truth: this package parses it at test time
// (no code generation, no hand-copied route list) and, for every path it
// finds, asserts that a running handler:
//
//   - resolves the path (200, not 404) once fixture values are substituted
//     for path parameters;
//   - accepts every documented query parameter, using an enum value where
//     the parameter declares one;
//   - returns every `required` property of the response's data schema; and
//   - wraps the payload in the {message, data} envelope the spec describes.
//
// It also runs a spec-change detector (TestAllSpecPathsAreClassified): if
// openapi.yaml grows a path this suite does not yet know how to exercise,
// that test fails with instructions for the maintainer, rather than the
// suite silently going stale.
//
// This package is meant to stay upstreamable: spec.go and the assertions in
// conformance_test.go import only the standard library and gopkg.in/yaml.v3.
// The only file that imports this repository's internal packages is
// harness_test.go, whose sole job is spinning up an in-process instance of
// this repo's handler with seeded fixture data — swapping that one file is
// what it would take to point this suite at a different implementation of
// the same standard.
package conformance
