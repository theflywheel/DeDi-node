package conformance

// Runs the published DeDi conformance suite
// (github.com/theflywheel/dedi-conformance, vendored as a submodule under
// third_party/) against this node, in-process.
//
// In-process rather than the container deliberately: the suite only needs a
// base URL, so an httptest.Server is a complete implementation under test.
// That means no image build, no deployment to point at, and no separate CI
// job — this runs on every `go test ./...` like anything else, and a
// regression is caught by the same command that catches every other one.
//
// This is the same standard the sibling tests in this package check, measured
// from the outside by a tool that knows nothing about our internals. The two
// are worth keeping: the local tests can assert things only reachable from
// inside, while this one cannot accidentally be made to pass by changing our
// own expectations, because the expectations live in another repository.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/dedi-conformance/manifest"
	"github.com/theflywheel/dedi-conformance/openapi"
	"github.com/theflywheel/dedi-conformance/report"
	"github.com/theflywheel/dedi-conformance/suite"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/testdb"
)

// Fixture names this test seeds, mapped onto the suite's abstract roles below.
const (
	stdNamespace = "flywheel"
	stdRegistry  = "participants"
	stdRecord    = "bap.example.com"
	stdRotated   = "rotated.example.com"
	stdRevoked   = "gone.example.com"
	stdAbsent    = "no-such-namespace-9f3a"
)

// The two versions of stdRotated are stamped a year apart. Appending twice in
// quick succession would put both in the same second, and as_on is serialized
// as RFC 3339 — so the moment the suite asks about would be ambiguous, and the
// case would be measuring timestamp resolution rather than time-travel.
var (
	stdV1At = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	stdV2At = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
)

// subscriber is a Beckn_subscriber-shaped payload. The reference schema marks
// countries required, so it is present here: this test measures whether the
// node round-trips a conformant record, not whether our production seed data
// happens to be conformant (that is issue #21, and is about the seed script).
func subscriber(id string) []byte {
	b, err := json.Marshal(map[string]any{
		"subscriber_id":      id,
		"url":                "https://" + id + "/beckn",
		"type":               "BPP",
		"domain":             "retail",
		"countries":          []string{"IND"},
		"signing_public_key": "TZ8xHnQm2vN0pWq7RsYkLbCd4FgHjKlMnOpQrStUvWx=",
		"encr_public_key":    "aB3dEf5GhI7jKlM9nOpQrS1tUvW3xYz5AbC7dEf9GhI=",
	})
	if err != nil {
		panic(err)
	}
	return b
}

// startStandardServer seeds a node with one of everything the suite's roles
// name, and returns it serving over HTTP.
func startStandardServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}

	must := func(in store.AppendInput) {
		t.Helper()
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed %s/%s/%s: %v", in.Namespace, in.Registry, in.RecordName, err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: stdNamespace,
		PayloadRaw: []byte(`{"description":"Flywheel network","domain":"flywheel.in"}`),
		CreatedBy:  "conformance", CreatedAt: stdV1At})
	must(store.AppendInput{EntryType: "registry", Namespace: stdNamespace, Registry: stdRegistry,
		PayloadRaw: []byte(`{"description":"Beckn participants","schema":{"type":"object"}}`),
		CreatedBy:  "conformance", CreatedAt: stdV1At})

	must(store.AppendInput{EntryType: "record", Namespace: stdNamespace, Registry: stdRegistry,
		RecordName: stdRecord, PayloadRaw: subscriber(stdRecord),
		CreatedBy: "conformance", CreatedAt: stdV1At})

	// Two versions, a year apart, so there is a moment at which the answer
	// must differ from current.
	must(store.AppendInput{EntryType: "record", Namespace: stdNamespace, Registry: stdRegistry,
		RecordName: stdRotated, PayloadRaw: subscriber(stdRotated),
		CreatedBy: "conformance", CreatedAt: stdV1At})
	must(store.AppendInput{EntryType: "record", Namespace: stdNamespace, Registry: stdRegistry,
		RecordName: stdRotated, PayloadRaw: subscriber(stdRotated + "/rotated"),
		CreatedBy: "conformance", CreatedAt: stdV2At})

	// A revocation is a new version carrying the revoked state, never a
	// deletion — which is what lets the suite tell it from a record that
	// never existed.
	must(store.AppendInput{EntryType: "record", Namespace: stdNamespace, Registry: stdRegistry,
		RecordName: stdRevoked, PayloadRaw: subscriber(stdRevoked),
		CreatedBy: "conformance", CreatedAt: stdV1At})
	must(store.AppendInput{EntryType: "record", Namespace: stdNamespace, Registry: stdRegistry,
		RecordName: stdRevoked, PayloadRaw: subscriber(stdRevoked), State: "revoked",
		CreatedBy: "conformance", CreatedAt: stdV2At})

	skey, _, err := note.GenerateKey(rand.Reader, "conformance.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "conformance.dedi.local/log", Interval: time.Hour}
	srv := httptest.NewServer((&api.Server{Store: s, CP: cp, TTL: 300}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestAgainstTheStandardSuite is the one that matters: it is the published
// conformance tool's verdict on this node, not ours.
//
// The publication profile is not claimed here. It verifies a publisher's
// /.well-known/dedi.index.json over TLS at a real domain, which an
// httptest.Server on 127.0.0.1 cannot stand in for — claiming it and skipping
// would report conformance nobody measured.
func TestAgainstTheStandardSuite(t *testing.T) {
	srv := startStandardServer(t)

	m := &manifest.Manifest{
		BaseURL: srv.URL,
		Profiles: []string{
			manifest.ProfileCore,
			manifest.ProfileVersioning,
			manifest.ProfileBeckn,
		},
		Fixtures: map[string]string{
			manifest.Namespace:          stdNamespace,
			manifest.Registry:           stdRegistry,
			manifest.Record:             stdRecord,
			manifest.RecordWithVersions: stdRotated,
			manifest.RevokedRecord:      stdRevoked,
			manifest.AbsentNamespace:    stdAbsent,
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("the manifest this test builds is invalid: %v", err)
	}

	spec, err := openapi.LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	run := suite.New(m, spec, 30*time.Second).Execute()

	for _, res := range run.Results {
		switch res.Outcome {
		case report.Fail:
			t.Errorf("%s / %s\n  request: %s\n  %s\n  spec: %s",
				res.Profile, res.Case, res.Request, res.Detail, res.SpecRef)
		case report.Skip:
			// Not a failure, but never silent: a suite that skips everything
			// reports no failures, and that is what a vacuous run looks like.
			t.Logf("SKIP %s / %s — %s", res.Profile, res.Case, res.Detail)
		}
	}

	// Guard against the run doing nothing at all — an empty result set would
	// otherwise pass this test without a single request having been made.
	for _, p := range m.Profiles {
		if pass, _, _ := run.Counts(p); pass == 0 {
			t.Errorf("the %s profile reported no passing cases; it ran nothing", p)
		}
	}
	if testing.Verbose() {
		run.WriteHuman(os.Stdout)
	}
}
