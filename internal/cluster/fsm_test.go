package cluster

import (
	"testing"
	"time"
)

// Every replica must record the same signing time for the same checkpoint.
// Without the leader stamping it once at proposal, each follower writes the
// moment it happened to apply the entry — so /status shows a different signing
// history depending on which replica you ask, and a slow follower reports a
// signing delay that never happened.
func TestReplicatedCheckpointCarriesTheLeadersSigningTime(t *testing.T) {
	signed := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	b, err := encodeCommand(command{
		Kind: cmdSignCheckpoint, TreeSize: 7, RootHash: make([]byte, 32),
		NoteText: "note", SignedAt: signed,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeCommand(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SignedAt.Equal(signed) {
		t.Errorf("SignedAt = %v, want %v — the leader's signing time did not survive the command",
			got.SignedAt, signed)
	}
}
