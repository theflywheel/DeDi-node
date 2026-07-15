package merkle

import (
	"crypto/rand"
	"testing"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"
)

func TestCheckpointRoundTrip(t *testing.T) {
	var root tlog.Hash
	for i := range root {
		root[i] = byte(i)
	}
	text := FormatCheckpoint("example.org/log", 42, root)
	origin, size, got, err := ParseCheckpoint(text)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "example.org/log" || size != 42 || got != root {
		t.Fatalf("round trip mismatch: %q %d", origin, size)
	}
}

func TestSignCheckpointVerifies(t *testing.T) {
	skey, vkey, err := note.GenerateKey(rand.Reader, "test.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	var root tlog.Hash
	signed, err := SignCheckpoint(skey, "test.dedi.local/log", 7, root)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := note.NewVerifier(vkey)
	if err != nil {
		t.Fatal(err)
	}
	n, err := note.Open([]byte(signed), note.VerifierList(verifier))
	if err != nil {
		t.Fatalf("note verify: %v", err)
	}
	if _, size, _, err := ParseCheckpoint(n.Text); err != nil || size != 7 {
		t.Fatalf("body parse: size=%d err=%v", size, err)
	}
}
