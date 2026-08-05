package main

import (
	"crypto/rand"
	"testing"

	"golang.org/x/mod/sumdb/note"
)

func TestVerifierKeyForRecoversTheGeneratedKey(t *testing.T) {
	// The recovered key has to be byte-identical to the one keygen printed:
	// a witness compares it against the signature on a checkpoint, so a key
	// that merely looks right fails at the only moment it is used.
	for _, name := range []string{"dedi.local", "a.example.org/log", "node-b.beckn.try-dough.com/log"} {
		skey, want, err := note.GenerateKey(rand.Reader, name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := verifierKeyFor(skey)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: derived %q, keygen printed %q", name, got, want)
		}
	}
}

func TestVerifierKeyForRejectsThingsThatAreNotNodeKeys(t *testing.T) {
	_, vkey, err := note.GenerateKey(rand.Reader, "dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, skey string }{
		// Handing the public key back in is the likely operator slip, and it
		// must not silently produce something.
		{"a verifier key", vkey},
		{"empty", ""},
		{"a publisher key", "dGhpcyBpcyBub3QgYSBub3RlIGtleQ=="},
		{"truncated", "PRIVATE+KEY+dedi.local+deadbeef"},
		{"bad base64", "PRIVATE+KEY+dedi.local+deadbeef+not!base64"},
	} {
		if got, err := verifierKeyFor(tc.skey); err == nil {
			t.Errorf("%s: want an error, got key %q", tc.name, got)
		}
	}
}
