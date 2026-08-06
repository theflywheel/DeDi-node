// Package cluster replicates the dedid transparency log across a small set of
// nodes using Raft.
//
// What this buys, and what it does not: Raft is a crash-fault protocol. It
// keeps the log available when a replica dies, and it gives every replica the
// same ordered command stream so they compute the same tree. It does *not*
// make the log trustworthy — a quorum of replicas run by one operator will
// happily agree on whatever that operator says. Tamper-evidence comes from the
// witness ring and from clients checking proofs, exactly as it did before, and
// nothing here changes or weakens that. Availability and trust are separate
// properties and this package only supplies the first.
package cluster

import (
	"encoding/json"
	"fmt"

	"github.com/theflywheel/DeDi-node/internal/store"
)

type commandKind string

const (
	// cmdAppend adds an entry to the log.
	cmdAppend commandKind = "append"
	// cmdSignCheckpoint publishes a signed tree head. It goes through the log
	// rather than being written directly by whichever process felt like it, so
	// that the record of what has been signed is itself replicated — see fsm.go.
	cmdSignCheckpoint commandKind = "sign_checkpoint"
)

// command is the replicated unit. Everything the state transition depends on
// must be inside it: a replica may apply this months later, on a different
// machine, and must reach the same state as everyone else.
type command struct {
	Kind commandKind `json:"kind"`

	// Append
	Input *store.AppendInput `json:"input,omitempty"`

	// SignCheckpoint
	TreeSize int64  `json:"tree_size,omitempty"`
	RootHash []byte `json:"root_hash,omitempty"`
	NoteText string `json:"note_text,omitempty"`
}

func encodeCommand(c command) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode %s command: %w", c.Kind, err)
	}
	return b, nil
}

func decodeCommand(b []byte) (command, error) {
	var c command
	if err := json.Unmarshal(b, &c); err != nil {
		return command{}, fmt.Errorf("decode command: %w", err)
	}
	switch c.Kind {
	case cmdAppend:
		if c.Input == nil {
			return command{}, fmt.Errorf("append command carries no input")
		}
	case cmdSignCheckpoint:
		if len(c.RootHash) == 0 || c.NoteText == "" {
			return command{}, fmt.Errorf("sign_checkpoint command carries no signed note")
		}
	default:
		// A replica running older code than the leader would land here. It must
		// not skip the entry — skipping diverges its state from the rest of the
		// cluster silently, which is the failure this whole design is built to
		// avoid. The caller stops instead.
		return command{}, fmt.Errorf("%w: %q", errUnknownCommand, c.Kind)
	}
	return c, nil
}
