// Package merkle defines the canonical leaf encoding and checkpoint format
// for the dedid transparency log.
package merkle

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

// LeafBytes returns the canonical preimage hashed (via tlog.RecordHash) into
// the log. A JSON array keeps field order fixed; timestamps are UTC
// RFC3339Nano. Any change to this encoding is a breaking change to every
// existing proof — version the leading tag if it must evolve.
func LeafBytes(entryType, namespace, registry, recordName string, versionNum int32, digest []byte, createdBy string, createdAt time.Time) []byte {
	b, err := json.Marshal([]any{
		"dedi/v1/leaf",
		entryType, namespace, registry, recordName,
		versionNum,
		hex.EncodeToString(digest),
		createdBy,
		createdAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		panic(err) // strings and ints cannot fail to marshal
	}
	return b
}
