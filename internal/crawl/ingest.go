package crawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// MirrorField marks a namespace as a mirror of another publisher rather than
// something this node speaks for. Defined in internal/dedifile because that is
// where it is enforced: Build skips mirrored namespaces, so crawled data is
// never re-signed under our key and passed off as ours. Ingest enforces the
// other half — a crawl may not write into a namespace that is not marked, so it
// can never overwrite something we publish ourselves.
const MirrorField = dedifile.MirrorField

// ErrNamespaceOwned means the crawl wanted to write into a namespace this node
// publishes itself, or mirrors from a different domain.
var ErrNamespaceOwned = errors.New("namespace is not a mirror of this domain")

// Appender is the write path. In a cluster this is the Raft proposer, so
// crawled data replicates exactly like locally published data — a follower
// must not ingest its own copy, or three replicas would disagree about a
// directory none of them authored.
type Appender interface {
	Append(ctx context.Context, in store.AppendInput) (store.Entry, error)
}

// Ingested reports what one Ingest changed. Counting rather than logging so a
// caller can tell a no-op crawl (the common case, several times an hour) from a
// crawl that actually moved something.
type Ingested struct {
	Namespaces int
	Registries int
	Records    int
	Revoked    int
}

// Changed reports whether anything at all was written.
func (i Ingested) Changed() bool {
	return i.Namespaces+i.Registries+i.Records+i.Revoked > 0
}

func (i Ingested) String() string {
	return fmt.Sprintf("%d namespaces, %d registries, %d records, %d revoked",
		i.Namespaces, i.Registries, i.Records, i.Revoked)
}

// Ingester writes a verified Result into the log.
type Ingester struct {
	Store  *store.Store
	Writer Appender
}

// Ingest stores a crawl result so every record it carries answers at its
// {namespace}/{registry}/{record} triple — §13's third DeDi-server condition,
// and the reason this is worth doing at all: a reader asks this node the same
// question the same way whether the answer came from our own publisher plane or
// from someone else's files.
//
// Writes are skipped when the stored payload already matches, so a crawl loop
// that runs hourly against an unchanged publisher appends nothing. Without
// that the log would grow without bound at the crawl cadence and every
// checkpoint would advance on data that never changed.
func (in *Ingester) Ingest(ctx context.Context, res Result) (Ingested, error) {
	var out Ingested
	// Every file's namespace is checked before the first append, so a manifest
	// whose second file is refused does not leave its first file ingested (#90).
	// Fetch already verified every file; this is the other half of a crawl
	// being all-or-nothing.
	for _, f := range res.Files {
		if err := in.mayMirror(ctx, res, f.Namespace); err != nil {
			return out, err
		}
	}
	for _, f := range res.Files {
		n, err := in.ingestFile(ctx, res, f)
		if err != nil {
			return out, err
		}
		out.Namespaces += n.Namespaces
		out.Registries += n.Registries
		out.Records += n.Records
		out.Revoked += n.Revoked
	}
	return out, nil
}

// mayMirror refuses a namespace a crawl of res must not write into.
func (in *Ingester) mayMirror(ctx context.Context, res Result, ns string) error {
	if strings.HasPrefix(ns, "_") {
		// Underscore is this node's own bookkeeping prefix and is hidden from
		// the read plane; letting a remote publisher write there would let them
		// hide records from every surface that reads them.
		return fmt.Errorf("refusing to mirror namespace %q: the _ prefix is reserved for this node", ns)
	}
	existing, err := in.Store.Resolve(ctx, "namespace", ns, "", "", nil, nil)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner := mirrorOf(existing.PayloadRaw); owner != res.Domain {
		// The interesting failure. Two publishers claiming one namespace is
		// not something a crawler may resolve on its own — picking either
		// one would silently answer lookups from a source the operator did
		// not choose.
		return fmt.Errorf("%w: %q is %s, not a mirror of %s",
			ErrNamespaceOwned, ns, describeOwner(owner), res.Domain)
	}
	return nil
}

// ingestFile writes one file. Its namespace has already passed mayMirror.
func (in *Ingester) ingestFile(ctx context.Context, res Result, f dedifile.File) (Ingested, error) {
	var out Ingested
	ns, reg := f.Namespace, f.Registry.Name

	nsPayload, err := json.Marshal(map[string]any{
		"description": "mirrored from " + res.Domain,
		MirrorField:   res.Domain,
	})
	if err != nil {
		return out, err
	}
	switch _, err := in.Store.Resolve(ctx, "namespace", ns, "", "", nil, nil); {
	case errors.Is(err, store.ErrNotFound):
		if err := in.append(ctx, res, store.AppendInput{
			EntryType: "namespace", Namespace: ns, PayloadRaw: nsPayload,
		}); err != nil {
			return out, err
		}
		out.Namespaces++
	case err != nil:
		return out, err
	}

	// The registry entry carries the publisher's own signed envelope alongside
	// its schema, so §13's "serves records and signatures unaltered" holds: a
	// reader can re-verify the proof against the publisher's key without
	// trusting our copy.
	regPayload, err := json.Marshal(map[string]any{
		"description":  "mirrored from " + res.Domain,
		"schema":       f.Registry.Schema,
		MirrorField:    res.Domain,
		"source_url":   f.SourceURL,
		"next_update":  f.NextUpdate,
		"updated_at":   f.Registry.UpdatedAt,
		"publisher":    f.Publisher,
		"proof":        f.Proof,
		"dedi_version": f.DediVersion,
	})
	if err != nil {
		return out, err
	}
	regState := "active"
	if f.Registry.State != "live" {
		// §13 requires honouring registry state. A registry the publisher has
		// marked inactive is mirrored as archived rather than dropped, so the
		// change is visible as a state transition instead of as a disappearance.
		regState = "archived"
	}
	changed, err := in.upsert(ctx, res, store.AppendInput{
		EntryType: "registry", Namespace: ns, Registry: reg,
		PayloadRaw: regPayload, State: regState,
	})
	if err != nil {
		return out, err
	}
	if changed {
		out.Registries++
	}

	present := map[string]bool{}
	for _, rec := range f.Records {
		present[rec.RecordName] = true
		payload := rec.Details
		if len(payload) == 0 {
			payload = []byte(`{}`)
		}
		changed, err := in.upsert(ctx, res, store.AppendInput{
			EntryType: "record", Namespace: ns, Registry: reg,
			RecordName: rec.RecordName, PayloadRaw: payload, State: "live",
		})
		if err != nil {
			return out, err
		}
		if changed {
			out.Records++
		}
	}

	// A record that has left the file is withdrawn, not deleted.
	//
	// §5.1 says lifecycle for individual records lives in a negative registry,
	// and this node already publishes revocations that way (task #49). Mirroring
	// a disappearance as a local revocation keeps the two consistent: the record
	// stops resolving, and its history says when and why it stopped.
	gone, err := in.recordsNoLongerPublished(ctx, ns, reg, present)
	if err != nil {
		return out, err
	}
	for _, name := range gone {
		if err := in.append(ctx, res, store.AppendInput{
			EntryType: "record", Namespace: ns, Registry: reg, RecordName: name,
			PayloadRaw: []byte(`{"revocation_reason":"no longer published by ` + res.Domain + `"}`),
			State:      "revoked",
		}); err != nil {
			return out, err
		}
		out.Revoked++
	}
	return out, nil
}

// upsert writes only when the stored entry differs, and reports whether it did.
func (in *Ingester) upsert(ctx context.Context, res Result, cmd store.AppendInput) (bool, error) {
	existing, err := in.Store.Resolve(ctx, cmd.EntryType, cmd.Namespace, cmd.Registry, cmd.RecordName, nil, nil)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return false, err
	default:
		if existing.State == cmd.State && jsonSame(existing.PayloadRaw, cmd.PayloadRaw) {
			return false, nil
		}
	}
	if err := in.append(ctx, res, cmd); err != nil {
		return false, err
	}
	return true, nil
}

func (in *Ingester) append(ctx context.Context, res Result, cmd store.AppendInput) error {
	// Authorship names the source, so "where did this record come from" is
	// answerable from the log alone rather than from operator memory.
	cmd.CreatedBy = "crawler:" + res.Domain
	_, err := in.Writer.Append(ctx, cmd)
	return err
}

// recordsNoLongerPublished lists the live records we hold for a mirrored
// registry that the freshly crawled file did not contain.
func (in *Ingester) recordsNoLongerPublished(ctx context.Context, ns, reg string, present map[string]bool) ([]string, error) {
	const pageSize = 500
	var gone []string
	// Paginated rather than capped: a registry with more records than one page
	// would otherwise have its tail treated as "still published" forever, so a
	// withdrawal past the cap would never be mirrored.
	for page := 1; ; page++ {
		rows, total, err := in.Store.QueryRecords(ctx, ns, reg, store.QueryFilters{Page: page, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.State == "revoked" || present[r.Name] {
				continue
			}
			gone = append(gone, r.Name)
		}
		if len(rows) == 0 || page*pageSize >= total {
			break
		}
	}
	// Sorted so a crawl that withdraws several records produces the same
	// sequence of log entries on every replica.
	sort.Strings(gone)
	return gone, nil
}

// mirrorOf returns the domain a namespace is a mirror of, or "" if it is this
// node's own.
func mirrorOf(payload []byte) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(payload, &obj) != nil {
		return ""
	}
	var domain string
	if json.Unmarshal(obj[MirrorField], &domain) != nil {
		return ""
	}
	return domain
}

func describeOwner(owner string) string {
	if owner == "" {
		return "published by this node"
	}
	return "a mirror of " + owner
}

// jsonSame compares two payloads by their compacted bytes, so formatting
// differences between a stored copy and a freshly fetched one do not read as a
// change and append a version that says nothing.
func jsonSame(a, b []byte) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
