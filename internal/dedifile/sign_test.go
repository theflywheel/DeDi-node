package dedifile

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/mod/sumdb/note"
)

func TestFileJWSRoundTrips(t *testing.T) {
	priv, kid := testSigner(t)
	f := sampleFile(t, priv, kid)

	// Verify using the JWK carried inside the file itself, exactly as a
	// third-party verifier does at step 2 of docs/publishing-dedi-files.md
	// §7.3 (offline integrity check, before ever touching the manifest).
	pub, err := PublicKeyFromJWK(f.Publisher.Key)
	if err != nil {
		t.Fatalf("PublicKeyFromJWK: %v", err)
	}
	if err := VerifyFile(f, pub); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	// Tamper with a record after signing: verification must fail.
	tampered := f
	tampered.Records = append([]Record{}, f.Records...)
	tampered.Records[0].RecordName = "tampered"
	if err := VerifyFile(tampered, pub); err == nil {
		t.Fatal("VerifyFile accepted a tampered file")
	}

	// A different key must not verify the original signature.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(f, otherPub); err == nil {
		t.Fatal("VerifyFile accepted the wrong public key")
	}
}

func TestManifestJWSRoundTrips(t *testing.T) {
	priv, kid := testSigner(t)
	m := Manifest{
		DediVersion: DediVersion,
		Type:        "dedi-manifest",
		Domain:      "example.org",
		Keys:        []JWK{PublicJWK(kid, priv.Public().(ed25519.PublicKey))},
		UpdatedAt:   "2026-01-01T00:00:00Z",
		NextUpdate:  "2026-01-02T00:00:00Z",
		Files:       []ManifestEntry{{Registry: "public-keys", URL: "https://example.org/dedi-files/example.org/dedi.public-keys.json", Digest: "sha-256:aa"}},
	}
	signed, err := SignManifest(m, priv, kid)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	pub, err := PublicKeyFromJWK(signed.Keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(signed, pub); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	tampered := signed
	tampered.Domain = "attacker.example"
	if err := VerifyManifest(tampered, pub); err == nil {
		t.Fatal("VerifyManifest accepted a tampered manifest")
	}
}

func TestSignerFromNodeKeyIsStableAndKeyed(t *testing.T) {
	// note.GenerateKey produces "PRIVATE+KEY+<name>+<hash>+<base64>", the
	// exact format this node's identity key ships in (cmd/dedid keygen /
	// nodeKey); reuse it here rather than hand-rolling the format.
	skeyA, _, err := note.GenerateKey(rand.Reader, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	skeyB, _, err := note.GenerateKey(rand.Reader, "node-b")
	if err != nil {
		t.Fatal(err)
	}

	priv1, kid1, err := SignerFromNodeKey(skeyA)
	if err != nil {
		t.Fatalf("SignerFromNodeKey: %v", err)
	}
	priv2, kid2, err := SignerFromNodeKey(skeyB)
	if err != nil {
		t.Fatalf("SignerFromNodeKey: %v", err)
	}
	if kid1 == kid2 {
		t.Fatal("kid should be derived from the public key, not constant across different keys")
	}
	if len(priv1) != ed25519.PrivateKeySize || len(priv2) != ed25519.PrivateKeySize {
		t.Fatal("expected full Ed25519 private keys")
	}

	// Re-deriving from the exact same note key must reproduce the same kid
	// and key, since a manifest's proof.verification_method and
	// publisher.key.kid have to agree across rebuilds.
	priv1b, kid1b, err := SignerFromNodeKey(skeyA)
	if err != nil {
		t.Fatalf("SignerFromNodeKey: %v", err)
	}
	if kid1b != kid1 {
		t.Fatalf("kid not stable across rebuilds: %q vs %q", kid1, kid1b)
	}
	if string(priv1b) != string(priv1) {
		t.Fatal("re-derived private key differs")
	}
}
