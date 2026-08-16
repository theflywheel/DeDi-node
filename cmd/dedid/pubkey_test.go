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

func TestAConfiguredNodeKeyStillYieldsItsVerifierKey(t *testing.T) {
	// nodeKey returned an empty verifier key for a key supplied via DEDI_KEY or
	// DEDI_KEY_FILE, and only the self-provisioning path filled it in. The
	// consequences were nowhere near the size of the omission: a node deployed
	// from a key file could not enrol as a child at all — it presented an empty
	// key, the parent refused it as malformed, and it retried every fifteen
	// seconds forever while every health surface on it read fine — and it
	// published no verifier key of its own, so nobody could check its
	// checkpoints without asking it for the key out of band.
	skey, vkey, err := note.GenerateKey(rand.Reader, "beckn.mobility")
	if err != nil {
		t.Fatal(err)
	}
	got, gotV, err := withVerifier(skey)
	if err != nil {
		t.Fatalf("withVerifier: %v", err)
	}
	if got != skey {
		t.Errorf("private key altered: %q", got)
	}
	if gotV != vkey {
		t.Errorf("verifier key = %q, want %q", gotV, vkey)
	}
}

func TestAnUnusableConfiguredKeyFailsLoudly(t *testing.T) {
	// The other half: deriving must not paper over a corrupt key by returning
	// an empty verifier key, which is what made the bug above invisible.
	if _, _, err := withVerifier("PRIVATE+KEY+name+hash+not-base64"); err == nil {
		t.Fatal("a corrupt node key was accepted")
	}
}
