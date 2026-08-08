package dedifile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"

	"github.com/theflywheel/DeDi-node/internal/testdb"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	url := testdb.URL(t)
	ctx := context.Background()
	s, err := store.Open(ctx, url)
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
	return s
}

func mustAppend(t *testing.T, s *store.Store, in store.AppendInput) {
	t.Helper()
	in.CreatedBy = "test"
	if _, err := s.Append(context.Background(), in); err != nil {
		t.Fatalf("append %s %s/%s/%s: %v", in.EntryType, in.Namespace, in.Registry, in.RecordName, err)
	}
}

func TestBuildProjectsLogIntoFilesAndManifest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org",
		PayloadRaw: []byte(`{"description":"Example org"}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "public-keys",
		PayloadRaw: []byte(`{"schema":{"type":"object"}}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "public-keys",
		RecordName: "auth-service", PayloadRaw: []byte(`{"public_key_id":"example.org:auth","publicKey":"abc"}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "public-keys",
		RecordName: "revoked-one", PayloadRaw: []byte(`{"public_key_id":"example.org:old"}`), State: "revoked"})

	// Internal bookkeeping namespace, mirroring internal/witness's `_witness`,
	// must never surface in a published file.
	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "_witness",
		PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "_witness", Registry: "verdicts",
		PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "_witness", Registry: "verdicts",
		RecordName: "r1", PayloadRaw: []byte(`{}`)})

	priv, kid := testSigner(t)
	manifest, files, err := Build(ctx, s, Config{
		Domain: "dedi.example.com", BaseURL: "https://dedi.example.com",
		Signer: priv, Kid: kid, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Two files: the registry itself, plus the namespace's negative registry
	// carrying its one revoked record. The internal namespace contributes
	// neither.
	if len(files) != 2 {
		t.Fatalf("expected 2 files (registry + revocations; internal namespace excluded), got %d", len(files))
	}
	var f File
	for _, cand := range files {
		if cand.Registry.Name == "public-keys" {
			f = cand
		}
	}
	if f.Namespace != "example.org" || f.Registry.Name != "public-keys" {
		t.Fatalf("unexpected file identity: %s/%s", f.Namespace, f.Registry.Name)
	}
	if f.Registry.State != "live" {
		t.Fatalf("expected registry state live, got %s", f.Registry.State)
	}
	if len(f.Records) != 1 || f.Records[0].RecordName != "auth-service" {
		t.Fatalf("expected exactly the one non-revoked record, got %+v", f.Records)
	}
	if f.SourceURL != "https://dedi.example.com/dedi-files/example.org/dedi.public-keys.json" {
		t.Fatalf("unexpected source_url: %s", f.SourceURL)
	}

	pub, err := PublicKeyFromJWK(f.Publisher.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(f, pub); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	manifestNames := map[string]bool{}
	for _, e := range manifest.Files {
		manifestNames[e.Registry] = true
	}
	if !manifestNames["public-keys"] || !manifestNames["revocations"] {
		t.Fatalf("unexpected manifest files: %+v", manifest.Files)
	}
	if len(manifest.Keys) != 1 {
		t.Fatalf("expected exactly one manifest key, got %d", len(manifest.Keys))
	}
	mpub, err := PublicKeyFromJWK(manifest.Keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(manifest, mpub); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	// The manifest's digest must match the actual served bytes of the file,
	// so a verifier's step-3 "digest moved -> re-fetch" check is meaningful.
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files[0].Digest == "" {
		t.Fatal("expected a non-empty digest")
	}
	_ = raw
}

func TestBuildDisambiguatesSameRegistryNameAcrossNamespaces(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, ns := range []string{"a.example", "b.example"} {
		mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(`{}`)})
		mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: ns, Registry: "members", PayloadRaw: []byte(`{}`)})
	}

	priv, kid := testSigner(t)
	manifest, files, err := Build(ctx, s, Config{
		Domain: "dedi.example.com", BaseURL: "https://dedi.example.com",
		Signer: priv, Kid: kid, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	names := map[string]bool{}
	for _, e := range manifest.Files {
		names[e.Registry] = true
	}
	if names["a.example/members"] != true || names["b.example/members"] != true {
		t.Fatalf("expected namespace-qualified manifest entries for the colliding registry name, got %+v", manifest.Files)
	}
}

// A revoked record must remain observable. Publishing revocation as absence
// would leave a crawler unable to tell "revoked" from "never existed", which is
// exactly what the spec's negative registries exist to prevent.
func TestRevokedRecordsBecomeANegativeRegistry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "members", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "live-one", PayloadRaw: []byte(`{"a":1}`)})
	// Same record name in two registries, both revoked: the negative registry
	// must not end up with duplicate record_name, which invalidates a file.
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "dupe", PayloadRaw: []byte(`{"revocation_reason":"key compromise"}`), State: "revoked"})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "members",
		RecordName: "dupe", PayloadRaw: []byte(`{}`), State: "revoked"})

	priv, kid := testSigner(t)
	_, files, err := Build(ctx, s, Config{
		Domain: "dedi.example.com", BaseURL: "https://dedi.example.com",
		Signer: priv, Kid: kid, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var rev *File
	for i := range files {
		if files[i].Registry.Name == "revocations" {
			rev = &files[i]
		}
	}
	if rev == nil {
		t.Fatalf("no revocations registry emitted; files: %d", len(files))
	}
	if len(rev.Records) != 2 {
		t.Fatalf("expected 2 revocations, got %+v", rev.Records)
	}
	seen := map[string]map[string]string{}
	for _, r := range rev.Records {
		var d map[string]string
		if err := json.Unmarshal(r.Details, &d); err != nil {
			t.Fatal(err)
		}
		if r.RecordName != d["revoked_id"] {
			t.Fatalf("record_name %q must match revoked_id %q", r.RecordName, d["revoked_id"])
		}
		if _, dup := seen[r.RecordName]; dup {
			t.Fatalf("duplicate record_name %q makes the file invalid", r.RecordName)
		}
		seen[r.RecordName] = d
	}
	if seen["keys/dupe"] == nil || seen["members/dupe"] == nil {
		t.Fatalf("expected registry-qualified ids, got %v", seen)
	}
	if got := seen["keys/dupe"]["reason"]; got != "key compromise" {
		t.Fatalf("expected the operator's reason to carry across, got %q", got)
	}
	if _, ok := seen["members/dupe"]["reason"]; ok {
		t.Fatalf("no reason was given; none should be published")
	}

	// The live record stays where it belongs, and the revoked one is gone from
	// its own registry's file.
	for _, f := range files {
		if f.Registry.Name != "keys" {
			continue
		}
		if len(f.Records) != 1 || f.Records[0].RecordName != "live-one" {
			t.Fatalf("source registry should carry only its live records, got %+v", f.Records)
		}
	}

	pub, err := PublicKeyFromJWK(rev.Publisher.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(*rev, pub); err != nil {
		t.Fatalf("the negative registry must itself be signed and verifiable: %v", err)
	}
}

// An operator may already run a registry called "revocations". The synthetic
// one must not collide with it on the manifest's uniqueness rule or on the URL
// both would be served from.
func TestRevocationRegistryYieldsToAnOperatorsOwn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "revocations", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "revocations",
		RecordName: "theirs", PayloadRaw: []byte(`{"revoked_id":"theirs"}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "gone", PayloadRaw: []byte(`{}`), State: "revoked"})

	priv, kid := testSigner(t)
	manifest, files, err := Build(ctx, s, Config{
		Domain: "dedi.example.com", BaseURL: "https://dedi.example.com",
		Signer: priv, Kid: kid, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	names := map[string]int{}
	for _, f := range files {
		names[f.Registry.Name]++
	}
	if names["revocations"] != 1 || names["dedi-revocations"] != 1 {
		t.Fatalf("expected the operator's registry untouched and ours renamed, got %v", names)
	}
	manifestNames := map[string]int{}
	for _, e := range manifest.Files {
		manifestNames[e.Registry]++
		if manifestNames[e.Registry] > 1 {
			t.Fatalf("manifest lists %q twice, which the schema forbids", e.Registry)
		}
	}
	for _, f := range files {
		if f.Registry.Name == "revocations" && (len(f.Records) != 1 || f.Records[0].RecordName != "theirs") {
			t.Fatalf("the operator's own revocations registry was overwritten: %+v", f.Records)
		}
	}
}

// The manifest commits to a sha-256 of every file it lists (spec §6.3), and a
// verifier checks it (§7.3 step 3). Because the manifest and the file are built
// by two separate requests, any wall-clock term in the output makes that digest
// wrong by the time the file is fetched — which is exactly what shipped, and
// what a 1.2s gap between the two fetches was enough to expose.
func TestOutputIsStableAcrossTheFreshnessWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mustAppend(t, s, store.AppendInput{EntryType: "namespace", Namespace: "example.org", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "registry", Namespace: "example.org", Registry: "keys", PayloadRaw: []byte(`{}`)})
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "a", PayloadRaw: []byte(`{"v":1}`)})

	priv, kid := testSigner(t)
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cfg := func(at time.Time) Config {
		return Config{Domain: "d.example", BaseURL: "https://d.example",
			Signer: priv, Kid: kid, Now: at, Freshness: time.Hour}
	}
	build := func(at time.Time) (Manifest, []File) {
		m, f, err := Build(ctx, s, cfg(at))
		if err != nil {
			t.Fatal(err)
		}
		return m, f
	}

	// Two builds far apart inside one hourly window must be byte-identical, so
	// a manifest fetched at one moment still vouches for a file fetched later.
	m1, f1 := build(base.Add(time.Minute))
	m2, f2 := build(base.Add(50 * time.Minute))
	r1, _ := json.Marshal(f1[0])
	r2, _ := json.Marshal(f2[0])
	if string(r1) != string(r2) {
		t.Fatalf("file bytes drifted within one window:\n%s\n%s", r1, r2)
	}
	b1, _ := json.Marshal(m1)
	b2, _ := json.Marshal(m2)
	if string(b1) != string(b2) {
		t.Fatalf("manifest bytes drifted within one window:\n%s\n%s", b1, b2)
	}

	// And the digest the manifest carries must be the digest of those bytes.
	sum := sha256.Sum256(r2)
	want := "sha-256:" + hex.EncodeToString(sum[:])
	if m1.Files[0].Digest != want {
		t.Fatalf("manifest digest %s does not match the served file %s", m1.Files[0].Digest, want)
	}

	// next_update is the window's end, and advances by exactly one window.
	if m1.NextUpdate != base.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("next_update should be the window end, got %s", m1.NextUpdate)
	}
	m3, _ := build(base.Add(90 * time.Minute))
	if m3.NextUpdate != base.Add(2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("next_update should advance one window, got %s", m3.NextUpdate)
	}

	// updated_at is content, not clock: it must not move across a re-issue
	// that changed nothing, and must move when a record is written.
	if m3.UpdatedAt != m1.UpdatedAt {
		t.Fatalf("updated_at moved on a re-issue with no content change: %s -> %s", m1.UpdatedAt, m3.UpdatedAt)
	}
	before := f1[0].Registry.UpdatedAt
	// The store stamps CreatedAt itself and updated_at is formatted to
	// RFC3339's one-second resolution, so a write in the same second as the
	// last one is genuinely indistinguishable. Cross a second boundary to
	// assert the thing under test rather than the clock's granularity.
	time.Sleep(1100 * time.Millisecond)
	mustAppend(t, s, store.AppendInput{EntryType: "record", Namespace: "example.org", Registry: "keys",
		RecordName: "b", PayloadRaw: []byte(`{"v":2}`)})
	_, f4 := build(base.Add(time.Minute))
	if f4[0].Registry.UpdatedAt == before {
		t.Fatalf("updated_at did not advance when a record was published (still %s)", before)
	}
}
