// A module of its own, deliberately.
//
// The conformance tests depend on github.com/theflywheel/dedi-conformance,
// which is vendored as a git submodule and therefore reached by a `replace`
// to a local path. Putting that in the root go.mod made `go mod download`
// fail in the Dockerfile — the image build has no submodules and does not
// need them — which would have broken the released image to serve a
// test-only dependency.
//
// Nested here, the root module's dependency graph is untouched: `dedid`
// builds from go.mod alone, and this module is the only thing that has to
// know the suite exists.
module github.com/theflywheel/DeDi-node/conformance

go 1.25.0

require (
	github.com/theflywheel/DeDi-node v0.0.0
	github.com/theflywheel/dedi-conformance v0.0.0
	golang.org/x/mod v0.38.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/armon/go-metrics v0.4.1 // indirect
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/gowebpki/jcs v1.0.1 // indirect
	github.com/hashicorp/go-hclog v1.6.2 // indirect
	github.com/hashicorp/go-immutable-radix v1.0.0 // indirect
	github.com/hashicorp/go-metrics v0.5.4 // indirect
	github.com/hashicorp/go-msgpack/v2 v2.1.2 // indirect
	github.com/hashicorp/golang-lru v0.5.5-0.20210104140557-80c98217689d // indirect
	github.com/hashicorp/raft v1.7.3 // indirect
	github.com/hashicorp/raft-boltdb/v2 v2.3.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	go.etcd.io/bbolt v1.3.5 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// Both are in this working tree: the parent repo, and the conformance suite
// as a submodule. Pinning by path means the version measured against is the
// commit this repo records, not whatever the module proxy last served.
replace github.com/theflywheel/DeDi-node => ../

replace github.com/theflywheel/dedi-conformance => ../third_party/dedi-conformance
