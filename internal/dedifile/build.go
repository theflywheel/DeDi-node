package dedifile

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/refschemas"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// Config parameterizes one build of this node's file-publication artifacts.
type Config struct {
	// Domain is the publisher identity (host only, no scheme), embedded in
	// every file's publisher.domain and the manifest's domain.
	Domain string
	// BaseURL is scheme://host, used to build source_url and files[].url.
	// No trailing slash.
	BaseURL string
	// Signer is this node's Ed25519 identity key. Required.
	Signer ed25519.PrivateKey
	// Kid is the stable key id advertised alongside Signer's public half.
	Kid string
	// Now is the build time; defaults to time.Now() if zero. It sets which
	// freshness window the output falls in, never the timestamps directly —
	// see nextUpdate.
	Now time.Time
	// Freshness is the re-issue cadence: how long a published copy stays
	// valid, and the width of the window next_update is quantized to.
	Freshness time.Duration
}

func (c Config) now() time.Time {
	if c.Now.IsZero() {
		return time.Now().UTC()
	}
	return c.Now.UTC()
}

func (c Config) freshness() time.Duration {
	if c.Freshness <= 0 {
		return 24 * time.Hour
	}
	return c.Freshness
}

// nextUpdate is the end of the freshness window containing now, rather than
// now plus the freshness interval.
//
// The distinction is what makes a build reproducible. Files are built per
// request, and the manifest commits to a sha-256 of each file it lists (spec
// §6.3); with next_update computed as now+freshness, the manifest and the file
// are stamped by two different requests a second apart, their bytes differ, and
// the digest a crawler checks in §7.3 step 3 never matches. Quantizing means
// every build inside one window is byte-identical, so the digest holds and the
// ETag stops churning. next_update still advances once per window, which is the
// re-issue cadence §9 asks for.
func (c Config) nextUpdate() time.Time {
	f := c.freshness()
	return c.now().Truncate(f).Add(f)
}

// contentUpdatedAt is the newest content timestamp among the entries that go
// into one file.
//
// Spec §9 separates the two timestamps: next_update advances on every re-issue,
// updated_at "advances only when content actually changes" — that difference is
// how a re-published revocation list signals nothing is new, and §12 has
// monitors watching updated_at for regressions as a rollback defence. Deriving
// it from the log rather than the clock is what makes it mean that. Taking the
// registry entry's own timestamp alone would be the inverse bug: publishing or
// revoking a record would change the file's content without advancing it.
func contentUpdatedAt(registry store.Entry, records []store.Entry) time.Time {
	newest := registry.CreatedAt
	for _, r := range records {
		if r.CreatedAt.After(newest) {
			newest = r.CreatedAt
		}
	}
	return newest.UTC()
}

// entryKey identifies one (entry_type, namespace, registry, record_name)
// stream in the log; the map built from it keeps only the latest version.
type entryKey struct{ typ, ns, reg, rec string }

// snapshot reduces a full log (every version of every entry) to the latest
// version of each. AllEntries returns rows in seq order, so a simple
// overwrite as we walk them keeps the last write per key — exactly what
// store.Resolve (no version_id/as_on) would return for each, without one
// round trip per resource.
func snapshot(entries []store.Entry) map[entryKey]store.Entry {
	latest := make(map[entryKey]store.Entry, len(entries))
	for _, e := range entries {
		latest[entryKey{e.EntryType, e.Namespace, e.Registry, e.RecordName}] = e
	}
	return latest
}

// registryState maps this node's stored lifecycle states onto the DeDi file
// schema's two-valued enum. Only "active" is a live, authoritative registry;
// anything else (archived, revoked) is not.
func registryState(stored string) string {
	if stored == "active" {
		return "live"
	}
	return "inactive"
}

// revokeSchemaURL is the spec's own reference schema for negative lists. The
// vendored example (examples/dedi.revocations.json) references it by URL rather
// than inlining it, and so do we — pinned to a commit rather than to `main`,
// for the reason refschemas.URL explains.
var revokeSchemaURL = refschemas.URL("revoke")

// filePlan is one DeDi file to emit. It exists so a synthetic registry — one
// with no backing entry in the log, like the revocations list — can be built
// the same way as a real one.
type filePlan struct {
	ns, reg   string
	schema    any
	state     string
	updatedAt string
	records   []Record
}

// revocationRecord turns a revoked record into an entry in the namespace's
// negative registry.
//
// The id is qualified with the registry name because record names are unique
// only within their own registry, while every revocation in a namespace lands
// in one list — an unqualified name would collide across registries and the
// spec makes duplicate record_name grounds for rejecting the whole file.
//
// The reason, when the operator gave one, was merged into the stored payload by
// the revoke handler; carry it across and leave it out entirely when absent
// rather than publishing an empty string as if a reason had been given.
func revocationRecord(registry, record string, payload []byte) Record {
	id := registry + "/" + record
	details := map[string]string{"revoked_id": id}
	var stored struct {
		Reason string `json:"revocation_reason"`
	}
	if json.Unmarshal(payload, &stored) == nil && stored.Reason != "" {
		details["reason"] = stored.Reason
	}
	raw, _ := json.Marshal(details)
	return Record{RecordName: id, Details: raw}
}

// revocationRegistryName picks the name for a namespace's negative registry.
// "revocations" matches the spec's example, but an operator may already have a
// registry by that name; the file would then collide on both its URL and the
// manifest's uniqueness rule, so fall back rather than overwrite theirs.
func revocationRegistryName(ns string, taken map[[2]string]bool) string {
	for _, candidate := range []string{"revocations", "dedi-revocations"} {
		if !taken[[2]string{ns, candidate}] {
			taken[[2]string{ns, candidate}] = true
			return candidate
		}
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("dedi-revocations-%d", i)
		if !taken[[2]string{ns, candidate}] {
			taken[[2]string{ns, candidate}] = true
			return candidate
		}
	}
}

func sortedKeys(m map[string][]Record) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type registryMeta struct {
	Description string         `json:"description"`
	Schema      any            `json:"schema"`
	Meta        map[string]any `json:"meta"`
}

func parseRegistryMeta(raw []byte) any {
	var p registryMeta
	if err := json.Unmarshal(raw, &p); err != nil || p.Schema == nil {
		return map[string]any{}
	}
	return p.Schema
}

// Build projects the current log into one signed DeDi file per (namespace,
// registry) plus a signed manifest listing them. Namespaces whose name
// begins with "_" are this node's own bookkeeping (see
// internal/api/internal_ns.go) and are skipped, matching what the spec read
// endpoints already hide.
func Build(ctx context.Context, st *store.Store, cfg Config) (Manifest, []File, error) {
	if cfg.Signer == nil {
		return Manifest{}, nil, fmt.Errorf("dedifile: Config.Signer is required")
	}
	if cfg.Domain == "" || cfg.BaseURL == "" {
		return Manifest{}, nil, fmt.Errorf("dedifile: Config.Domain and Config.BaseURL are required")
	}
	entries, err := st.AllEntries(ctx)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("load log: %w", err)
	}
	latest := snapshot(entries)

	pub := ed25519.PrivateKey(cfg.Signer).Public().(ed25519.PublicKey)
	jwk := PublicJWK(cfg.Kid, pub)

	now := cfg.now()
	nextUpdate := cfg.nextUpdate().Format(time.RFC3339)

	// Group records by (namespace, registry) so each registry's file lists
	// every one of its current, live records.
	//
	// A revoked record is not simply omitted. The file schema has no per-record
	// state — lifecycle lives at the registry level and, for individual records,
	// in a *negative registry* (publishing-dedi-files.md §5.1) — so dropping a
	// revoked record would publish revocation as absence, which a crawler cannot
	// tell apart from never-existed. Instead they are collected here and emitted
	// per namespace as a revocations registry below.
	// The log entries are carried alongside, not just the projected Records,
	// because updated_at is derived from their timestamps (contentUpdatedAt).
	recordsByRegistry := map[[2]string][]Record{}
	entriesByRegistry := map[[2]string][]store.Entry{}
	revokedByNS := map[string][]Record{}
	revokedEntriesByNS := map[string][]store.Entry{}
	for k, e := range latest {
		if k.typ != "record" {
			continue
		}
		if e.State == "revoked" {
			revokedByNS[k.ns] = append(revokedByNS[k.ns], revocationRecord(k.reg, k.rec, e.PayloadRaw))
			revokedEntriesByNS[k.ns] = append(revokedEntriesByNS[k.ns], e)
			continue
		}
		key := [2]string{k.ns, k.reg}
		recordsByRegistry[key] = append(recordsByRegistry[key],
			Record{RecordName: k.rec, Details: json.RawMessage(e.PayloadRaw)})
		entriesByRegistry[key] = append(entriesByRegistry[key], e)
	}
	for key := range recordsByRegistry {
		recs := recordsByRegistry[key]
		sort.Slice(recs, func(i, j int) bool { return recs[i].RecordName < recs[j].RecordName })
		recordsByRegistry[key] = recs
	}

	// Namespaces, sorted for deterministic output.
	type nsReg struct{ ns, reg string }
	var pairs []nsReg
	for k, e := range latest {
		if k.typ != "registry" {
			continue
		}
		if strings.HasPrefix(k.ns, "_") {
			continue // internal bookkeeping namespace; hidden from spec surfaces too
		}
		// The namespace itself must still exist (any state); a registry entry
		// cannot outlive its parent in this log, but guard against a
		// concurrent read of a half-applied restore anyway.
		if _, ok := latest[entryKey{"namespace", k.ns, "", ""}]; !ok {
			continue
		}
		_ = e
		pairs = append(pairs, nsReg{k.ns, k.reg})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].ns != pairs[j].ns {
			return pairs[i].ns < pairs[j].ns
		}
		return pairs[i].reg < pairs[j].reg
	})

	// Plan one file per real registry, then append a synthetic revocations
	// registry for each namespace that has any revoked record.
	plans := make([]filePlan, 0, len(pairs)+len(revokedByNS))
	taken := map[[2]string]bool{}
	for _, p := range pairs {
		regEntry := latest[entryKey{"registry", p.ns, p.reg, ""}]
		taken[[2]string{p.ns, p.reg}] = true
		plans = append(plans, filePlan{
			ns:     p.ns,
			reg:    p.reg,
			schema: parseRegistryMeta(regEntry.PayloadRaw),
			state:  registryState(regEntry.State),
			updatedAt: contentUpdatedAt(regEntry, entriesByRegistry[[2]string{p.ns, p.reg}]).
				Format(time.RFC3339),
			records: recordsByRegistry[[2]string{p.ns, p.reg}],
		})
	}
	for _, ns := range sortedKeys(revokedByNS) {
		if strings.HasPrefix(ns, "_") {
			continue
		}
		if _, ok := latest[entryKey{"namespace", ns, "", ""}]; !ok {
			continue
		}
		recs := revokedByNS[ns]
		sort.Slice(recs, func(i, j int) bool { return recs[i].RecordName < recs[j].RecordName })
		plans = append(plans, filePlan{
			ns:  ns,
			reg: revocationRegistryName(ns, taken),
			// The bundled reference schema for negative lists, by URL rather
			// than inline: the spec's own example references it the same way,
			// and it is the schema internal/refschemas serves as "revoke".
			schema: revokeSchemaURL,
			state:  "live",
			// From the revocations themselves, so a list re-issued on a
			// cadence presents a moving next_update over a static updated_at —
			// the "nothing new" signal of §9.
			updatedAt: contentUpdatedAt(store.Entry{}, revokedEntriesByNS[ns]).Format(time.RFC3339),
			records:   recs,
		})
	}
	sort.SliceStable(plans, func(i, j int) bool {
		if plans[i].ns != plans[j].ns {
			return plans[i].ns < plans[j].ns
		}
		return plans[i].reg < plans[j].reg
	})

	regNameCount := map[string]int{}
	for _, p := range plans {
		regNameCount[p.reg]++
	}

	files := make([]File, 0, len(plans))
	manifestEntries := make([]ManifestEntry, 0, len(plans))

	for _, p := range plans {
		schema := p.schema
		f := File{
			DediVersion: DediVersion,
			Type:        "dedi-file",
			SourceURL:   fmt.Sprintf("%s/dedi-files/%s/dedi.%s.json", cfg.BaseURL, p.ns, p.reg),
			NextUpdate:  nextUpdate,
			Publisher:   Publisher{Domain: cfg.Domain, Key: jwk},
			Namespace:   p.ns,
			Registry: Registry{
				Name:      p.reg,
				Schema:    schema,
				State:     p.state,
				UpdatedAt: p.updatedAt,
			},
			Records: p.records,
		}
		if f.Records == nil {
			f.Records = []Record{}
		}
		signed, err := SignFile(f, cfg.Signer, cfg.Kid)
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("sign file %s/%s: %w", p.ns, p.reg, err)
		}
		files = append(files, signed)

		raw, err := json.Marshal(signed)
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("marshal file %s/%s: %w", p.ns, p.reg, err)
		}
		digest := sha256.Sum256(raw)

		// The manifest is per-domain but this node is multi-tenant across
		// namespaces, so the bare registry name is not guaranteed unique the
		// way it would be for a single-namespace publisher. Qualify it with
		// the namespace to satisfy the manifest's own uniqueness requirement
		// (dedi-manifest.schema.json) without changing the file's own
		// registry.name, which is namespace-relative by design.
		manifestName := p.reg
		if regNameCount[p.reg] > 1 {
			manifestName = p.ns + "/" + p.reg
		}

		manifestEntries = append(manifestEntries, ManifestEntry{
			Registry: manifestName,
			URL:      f.SourceURL,
			Digest:   "sha-256:" + hex.EncodeToString(digest[:]),
			Schema:   schema,
			State:    f.Registry.State,
		})
	}

	// The manifest's own updated_at is the newest content it vouches for, for
	// the same reason each file's is: it must move when the directory changes
	// and hold still when it does not. With nothing published yet there is no
	// content to date it from, so the build time stands in.
	manifestUpdatedAt := time.Time{}
	for _, p := range plans {
		if t, err := time.Parse(time.RFC3339, p.updatedAt); err == nil && t.After(manifestUpdatedAt) {
			manifestUpdatedAt = t
		}
	}
	if manifestUpdatedAt.IsZero() {
		manifestUpdatedAt = now
	}

	m := Manifest{
		DediVersion: DediVersion,
		Type:        "dedi-manifest",
		Domain:      cfg.Domain,
		Keys:        []JWK{jwk},
		UpdatedAt:   manifestUpdatedAt.UTC().Format(time.RFC3339),
		NextUpdate:  nextUpdate,
		Files:       manifestEntries,
	}
	signedManifest, err := SignManifest(m, cfg.Signer, cfg.Kid)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("sign manifest: %w", err)
	}
	return signedManifest, files, nil
}
