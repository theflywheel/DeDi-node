// Package testdb resolves the database URL the repo's tests run against.
//
// It exists to make "no database" a loud result. Every database-backed test
// used to call t.Skip when TEST_DATABASE_URL was unset, and `go test` prints
// "ok" for a package that skipped every test in it — 108 of internal/api's 122,
// and three of the conformance suite's four, including every assertion that
// touches the spec. A suite that has verified nothing then reads exactly like a
// suite that passed. That is not hypothetical: it is how the wall-clock bug in
// internal/dedifile (docs/spec-gaps.md G1) shipped past a green run.
//
// So the default is to fail. Skipping is still available, but only when asked
// for explicitly — `go test -short`, or DEDI_TEST_SKIP_WITHOUT_DB=1 — so that
// skipping is a decision someone made rather than something that quietly
// happened.
package testdb

import (
	"os"
	"testing"
)

// EnvVar is the environment variable holding the test database URL.
const EnvVar = "TEST_DATABASE_URL"

const skipVar = "DEDI_TEST_SKIP_WITHOUT_DB"

const missing = EnvVar + ` is not set, so this test cannot run.

Start one and point at it, e.g.:

    docker compose up -d db
    export ` + EnvVar + `='postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable'

To skip database-backed tests deliberately instead, run with -short or set ` +
	skipVar + `=1.`

// URL returns the test database URL, failing the test if there is none.
//
// Use this in place of reading the environment directly: the point is that
// exactly one place decides what "no database" means.
func URL(t testing.TB) string {
	t.Helper()
	if url := os.Getenv(EnvVar); url != "" {
		return url
	}
	if testing.Short() || os.Getenv(skipVar) != "" {
		t.Skip(EnvVar + " not set; skipping because -short/" + skipVar + " was given")
	}
	t.Fatal(missing)
	return ""
}
