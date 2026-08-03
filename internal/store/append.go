package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	EntryType           string // namespace | registry | record
	Namespace           string
	Registry            string
	RecordName          string
	PayloadRaw          []byte // JSON; compacted before storing/hashing
	State               string // default: record→live, namespace/registry→active
	CreatedBy           string
	ExpectedPrevVersion *int32
}

func validateAppend(in *AppendInput) error {
	switch in.EntryType {
	case "namespace":
		if in.Namespace == "" || in.Registry != "" || in.RecordName != "" {
			return fmt.Errorf("namespace entry needs namespace only")
		}
		if in.State == "" {
			in.State = "active"
		}
	case "registry":
		if in.Namespace == "" || in.Registry == "" || in.RecordName != "" {
			return fmt.Errorf("registry entry needs namespace and registry")
		}
		if in.State == "" {
			in.State = "active"
		}
	case "record":
		if in.Namespace == "" || in.Registry == "" || in.RecordName == "" {
			return fmt.Errorf("record entry needs namespace, registry and record_name")
		}
		if in.State == "" {
			in.State = "live"
		}
	default:
		return fmt.Errorf("invalid entry_type %q", in.EntryType)
	}
	if !json.Valid(in.PayloadRaw) {
		return fmt.Errorf("payload is not valid JSON")
	}
	return nil
}

func (s *Store) Append(ctx context.Context, in AppendInput) (Entry, error) {
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
	}

	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq)+1, 0) FROM log_entries`).Scan(&seq); err != nil {
		return Entry{}, err
	}
	var currentVersion int32
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_num), 0) FROM log_entries
		 WHERE entry_type=$1 AND namespace=$2 AND registry=$3 AND record_name=$4`,
		in.EntryType, in.Namespace, in.Registry, in.RecordName).Scan(&currentVersion); err != nil {
		return Entry{}, err
	}
	if in.ExpectedPrevVersion != nil && *in.ExpectedPrevVersion != currentVersion {
		return Entry{}, fmt.Errorf("%w: expected previous version %d, got %d",
			ErrVersionConflict, *in.ExpectedPrevVersion, currentVersion)
	}
	vnum := currentVersion + 1

	// Truncate to Postgres timestamptz precision so the stored value
	// reproduces the hashed leaf bytes exactly.
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
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
