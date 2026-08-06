package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

// logWriteLock serializes all appends; seq assignment depends on it.
const logWriteLock int64 = 0x64656469 // "dedi"

type Entry struct {
	Seq        int64
	EntryType  string
	Namespace  string
	Registry   string
	RecordName string
	VersionNum int32
	PayloadRaw []byte
	Digest     []byte
	State      string
	CreatedBy  string
	CreatedAt  time.Time
	LeafHash   []byte
}

type AppendInput struct {
	EntryType  string // namespace | registry | record
	Namespace  string
	Registry   string
	RecordName string
	PayloadRaw []byte // JSON; compacted before storing/hashing
	State      string // default: record→live, namespace/registry→active
	CreatedBy  string

	// Precondition on the resource's current state, evaluated inside the append
	// transaction under the write lock. Checking it any earlier would be a
	// time-of-check race: two writers could both read the same current version
	// and both append, which is exactly the lost update the precondition exists
	// to prevent — and exactly what makes a captured signed write replayable.
	//
	// At most one may be set.
	ExpectedPrevDigest []byte // the latest version's digest must equal this
	ExpectedPrevState  string // when set, the latest version's state must equal this too
	ExpectedAbsent     bool   // no version may exist yet

	// CreatedAt stamps the entry. It is part of the Merkle leaf preimage
	// (merkle.LeafBytes), so it must travel *with* the command rather than be
	// read from the clock at apply time: under replication the same command is
	// applied on every replica, and three clocks would produce three different
	// leaf hashes and three different roots for the same entry. That divergence
	// is silent until a consistency proof fails, which is the most expensive
	// moment to discover it.
	//
	// Zero means "stamp it now", which is what a single unreplicated node wants
	// and what Append does. Apply requires it to be set.
	CreatedAt time.Time
}

// checkPrecondition compares the caller's expectation against the resource as
// it stands inside the transaction. currentDigest is nil when nothing has been
// published under this name yet.
func checkPrecondition(in AppendInput, currentDigest []byte, currentState string) error {
	switch {
	case in.ExpectedAbsent && currentDigest != nil:
		return fmt.Errorf("%w: expected no existing version, found %x-%s",
			ErrVersionConflict, currentDigest, currentState)
	case in.ExpectedPrevDigest != nil && currentDigest == nil:
		return fmt.Errorf("%w: expected version %x-%s, found none",
			ErrVersionConflict, in.ExpectedPrevDigest, in.ExpectedPrevState)
	case in.ExpectedPrevDigest != nil && !bytes.Equal(in.ExpectedPrevDigest, currentDigest):
		return fmt.Errorf("%w: expected version %x-%s, found %x-%s",
			ErrVersionConflict, in.ExpectedPrevDigest, in.ExpectedPrevState, currentDigest, currentState)
	case in.ExpectedPrevDigest != nil && in.ExpectedPrevState != "" && in.ExpectedPrevState != currentState:
		return fmt.Errorf("%w: expected version %x-%s, found %x-%s",
			ErrVersionConflict, in.ExpectedPrevDigest, in.ExpectedPrevState, currentDigest, currentState)
	}
	return nil
}

func validateAppend(in *AppendInput) error {
	switch in.EntryType {
	case "namespace":
		if in.Namespace == "" || in.Registry != "" || in.RecordName != "" {
			return fmt.Errorf("%w: namespace entry needs namespace only", ErrInvalidWrite)
		}
		if in.State == "" {
			in.State = "active"
		}
	case "registry":
		if in.Namespace == "" || in.Registry == "" || in.RecordName != "" {
			return fmt.Errorf("%w: registry entry needs namespace and registry", ErrInvalidWrite)
		}
		if in.State == "" {
			in.State = "active"
		}
	case "record":
		if in.Namespace == "" || in.Registry == "" || in.RecordName == "" {
			return fmt.Errorf("%w: record entry needs namespace, registry and record_name", ErrInvalidWrite)
		}
		if in.State == "" {
			in.State = "live"
		}
	default:
		return fmt.Errorf("%w: invalid entry_type %q", ErrInvalidWrite, in.EntryType)
	}
	if !json.Valid(in.PayloadRaw) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrInvalidWrite)
	}
	// Every entry must name its author. created_by is part of the Merkle leaf
	// preimage (merkle.LeafBytes), so authorship is covered by inclusion
	// proofs — an unattributable entry would weaken the audit trail
	// governance.md requires, and a side column would not be provable.
	if strings.TrimSpace(in.CreatedBy) == "" {
		return fmt.Errorf("%w: created_by is required — every log entry must name its author", ErrInvalidWrite)
	}
	if in.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at is required — it is hashed into the leaf, so it must be "+
			"decided once by the proposer, not re-read from each replica's clock", ErrInvalidWrite)
	}
	return nil
}

// Append stamps the entry with this node's clock and applies it. It is the
// entry point for an unreplicated node; a replicated one stamps the command
// before proposing it and calls Apply on every replica.
func (s *Store) Append(ctx context.Context, in AppendInput) (Entry, error) {
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	return s.Apply(ctx, in)
}

// Apply is the log's state transition: a pure function of the current log state
// and the command, modulo the database it writes to. Given the same prior
// entries and the same ordered commands it produces byte-identical leaves,
// tree hashes and roots on every replica — which is what makes the log
// replicable at all. Nothing in here may consult a clock, a random source, or
// any state outside the transaction.
func (s *Store) Apply(ctx context.Context, in AppendInput) (Entry, error) {
	if err := validateAppend(&in); err != nil {
		return Entry{}, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, in.PayloadRaw); err != nil {
		return Entry{}, fmt.Errorf("compact payload: %w", err)
	}
	raw := append([]byte(nil), buf.Bytes()...)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return Entry{}, err
	}

	// Parent must exist (any version).
	switch in.EntryType {
	case "registry":
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM log_entries WHERE entry_type='namespace' AND namespace=$1)`, in.Namespace).Scan(&ok); err != nil {
			return Entry{}, err
		}
		if !ok {
			return Entry{}, fmt.Errorf("namespace %q: %w", in.Namespace, ErrNotFound)
		}
	case "record":
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM log_entries WHERE entry_type='registry' AND namespace=$1 AND registry=$2)`, in.Namespace, in.Registry).Scan(&ok); err != nil {
			return Entry{}, err
		}
		if !ok {
			return Entry{}, fmt.Errorf("registry %s/%s: %w", in.Namespace, in.Registry, ErrNotFound)
		}
		schema, err := registrySchemaFrom(ctx, tx, in.Namespace, in.Registry)
		if err != nil {
			return Entry{}, err
		}
		if err := ValidateAgainstSchema(schema, in.PayloadRaw); err != nil {
			return Entry{}, err
		}
	}

	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq)+1, 0) FROM log_entries`).Scan(&seq); err != nil {
		return Entry{}, err
	}
	var currentVersion int32
	var currentDigest []byte
	var currentState string
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_num), 0),
		        (SELECT digest FROM log_entries
		          WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4
		          ORDER BY version_num DESC LIMIT 1),
		        COALESCE((SELECT state FROM log_entries
		          WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4
		          ORDER BY version_num DESC LIMIT 1), '')
		   FROM log_entries
		  WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4`,
		in.EntryType, in.Namespace, in.Registry, in.RecordName).Scan(&currentVersion, &currentDigest, &currentState); err != nil {
		return Entry{}, err
	}
	if err := checkPrecondition(in, currentDigest, currentState); err != nil {
		return Entry{}, err
	}
	vnum := currentVersion + 1

	// Truncate to Postgres timestamptz precision so the stored value reproduces
	// the hashed leaf bytes exactly. Normalising here rather than trusting the
	// proposer keeps the leaf a function of the command alone: two replicas
	// cannot disagree because one of them rounded.
	createdAt := in.CreatedAt.UTC().Truncate(time.Microsecond)
	digest := sha256.Sum256(raw)
	leafBytes := merkle.LeafBytes(in.EntryType, in.Namespace, in.Registry, in.RecordName, vnum, digest[:], in.CreatedBy, createdAt)
	leafHash := tlog.RecordHash(leafBytes)

	if _, err := tx.Exec(ctx,
		`INSERT INTO log_entries (seq, entry_type, namespace, registry, record_name, version_num,
		   payload_raw, payload, digest, state, created_by, created_at, leaf_hash)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13)`,
		seq, in.EntryType, in.Namespace, in.Registry, in.RecordName, vnum,
		raw, string(raw), digest[:], in.State, in.CreatedBy, createdAt, leafHash[:]); err != nil {
		return Entry{}, err
	}

	hashes, err := tlog.StoredHashes(seq, leafBytes, hashReader{ctx, tx})
	if err != nil {
		return Entry{}, fmt.Errorf("stored hashes for seq %d: %w", seq, err)
	}
	base := tlog.StoredHashCount(seq)
	for i, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO tree_hashes (idx, hash) VALUES ($1,$2)`, base+int64(i), h[:]); err != nil {
			return Entry{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, err
	}
	return Entry{
		Seq: seq, EntryType: in.EntryType, Namespace: in.Namespace, Registry: in.Registry,
		RecordName: in.RecordName, VersionNum: vnum, PayloadRaw: raw, Digest: digest[:],
		State: in.State, CreatedBy: in.CreatedBy, CreatedAt: createdAt, LeafHash: leafHash[:],
	}, nil
}
