package dedifile

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gowebpki/jcs"
)

// detachedHeader is fixed: EdDSA over an unencoded (RFC 7797 b64:false)
// detached payload, matching docs/publishing-dedi-files.md section 7.2 and
// the example jws in the spec's worked examples.
const detachedHeader = `{"alg":"EdDSA","b64":false,"crit":["b64"]}`

// fileNoProof and manifestNoProof mirror File and Manifest with the proof
// field removed structurally (rather than by round-tripping through a
// generic map, which would turn JSON numbers in record payloads into
// float64 and risk re-serializing them differently). The signing input is
// JCS(document - proof) per section 7.1.
type fileNoProof struct {
	DediVersion string    `json:"dedi_version"`
	Type        string    `json:"type"`
	SourceURL   string    `json:"source_url"`
	NextUpdate  string    `json:"next_update"`
	Publisher   Publisher `json:"publisher"`
	Namespace   string    `json:"namespace"`
	Registry    Registry  `json:"registry"`
	Records     []Record  `json:"records"`
}

type manifestNoProof struct {
	DediVersion string          `json:"dedi_version"`
	Type        string          `json:"type,omitempty"`
	Domain      string          `json:"domain"`
	Name        string          `json:"name,omitempty"`
	Keys        []JWK           `json:"keys"`
	UpdatedAt   string          `json:"updated_at"`
	NextUpdate  string          `json:"next_update"`
	Files       []ManifestEntry `json:"files"`
}

// signingBytesFile returns the JCS-canonicalized bytes of f with its proof
// removed — the exact input the detached JWS is computed and verified over.
func signingBytesFile(f File) ([]byte, error) {
	n := fileNoProof{f.DediVersion, f.Type, f.SourceURL, f.NextUpdate, f.Publisher, f.Namespace, f.Registry, f.Records}
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(raw)
}

func signingBytesManifest(m Manifest) ([]byte, error) {
	n := manifestNoProof{m.DediVersion, m.Type, m.Domain, m.Name, m.Keys, m.UpdatedAt, m.NextUpdate, m.Files}
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return jcs.Transform(raw)
}

// detachedSign produces proof.jws: a compact detached JWS (RFC 7515 +
// RFC 7797) over payload, signed with priv.
func detachedSign(priv ed25519.PrivateKey, payload []byte) string {
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(detachedHeader))
	signingInput := make([]byte, 0, len(headerB64)+1+len(payload))
	signingInput = append(signingInput, headerB64...)
	signingInput = append(signingInput, '.')
	signingInput = append(signingInput, payload...)
	sig := ed25519.Sign(priv, signingInput)
	return headerB64 + ".." + base64.RawURLEncoding.EncodeToString(sig)
}

// detachedVerify checks a compact detached JWS produced by detachedSign
// against payload and pub.
func detachedVerify(jws string, payload []byte, pub ed25519.PublicKey) error {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return fmt.Errorf("dedifile: malformed jws: expected 3 dot-separated parts, got %d", len(parts))
	}
	headerB64, mid, sigB64 := parts[0], parts[1], parts[2]
	if mid != "" {
		return fmt.Errorf("dedifile: jws is not detached: payload segment must be empty")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		return fmt.Errorf("dedifile: decode jws header: %w", err)
	}
	var hdr struct {
		Alg  string   `json:"alg"`
		B64  bool     `json:"b64"`
		Crit []string `json:"crit"`
	}
	if err := json.Unmarshal(headerRaw, &hdr); err != nil {
		return fmt.Errorf("dedifile: parse jws header: %w", err)
	}
	if hdr.Alg != "EdDSA" {
		return fmt.Errorf("dedifile: unsupported jws alg %q, want EdDSA", hdr.Alg)
	}
	if hdr.B64 {
		return fmt.Errorf("dedifile: jws header must declare b64:false for a detached, unencoded payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("dedifile: decode jws signature: %w", err)
	}
	signingInput := make([]byte, 0, len(headerB64)+1+len(payload))
	signingInput = append(signingInput, headerB64...)
	signingInput = append(signingInput, '.')
	signingInput = append(signingInput, payload...)
	if !ed25519.Verify(pub, signingInput, sig) {
		return fmt.Errorf("dedifile: signature verification failed")
	}
	return nil
}

// SignFile signs f, returning a copy with proof populated.
func SignFile(f File, priv ed25519.PrivateKey, kid string) (File, error) {
	payload, err := signingBytesFile(f)
	if err != nil {
		return File{}, fmt.Errorf("canonicalize file: %w", err)
	}
	f.Proof = Proof{
		VerificationMethod: kid,
		Canonicalization:   "JCS",
		JWS:                detachedSign(priv, payload),
	}
	return f, nil
}

// VerifyFile checks f.Proof.JWS against pub. It does not check that pub
// belongs to f.Publisher.Key or that f.Publisher.Key appears in any
// manifest — that is step 3 of docs/publishing-dedi-files.md section 7.3,
// which is a network fetch and out of scope for this offline check (step 2).
func VerifyFile(f File, pub ed25519.PublicKey) error {
	f2 := f
	f2.Proof = Proof{}
	payload, err := signingBytesFile(f2)
	if err != nil {
		return fmt.Errorf("canonicalize file: %w", err)
	}
	return detachedVerify(f.Proof.JWS, payload, pub)
}

// SignManifest signs m, returning a copy with proof populated.
func SignManifest(m Manifest, priv ed25519.PrivateKey, kid string) (Manifest, error) {
	payload, err := signingBytesManifest(m)
	if err != nil {
		return Manifest{}, fmt.Errorf("canonicalize manifest: %w", err)
	}
	m.Proof = Proof{
		VerificationMethod: kid,
		Canonicalization:   "JCS",
		JWS:                detachedSign(priv, payload),
	}
	return m, nil
}

// VerifyManifest checks m.Proof.JWS against pub.
func VerifyManifest(m Manifest, pub ed25519.PublicKey) error {
	m2 := m
	m2.Proof = Proof{}
	payload, err := signingBytesManifest(m2)
	if err != nil {
		return fmt.Errorf("canonicalize manifest: %w", err)
	}
	return detachedVerify(m.Proof.JWS, payload, pub)
}

// PublicJWK encodes an Ed25519 public key as an OKP JWK (RFC 8037) under the
// given kid.
func PublicJWK(kid string, pub ed25519.PublicKey) JWK {
	return JWK{Kid: kid, Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub)}
}

// PublicKeyFromJWK recovers the raw Ed25519 public key from an OKP JWK.
func PublicKeyFromJWK(jwk JWK) (ed25519.PublicKey, error) {
	if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
		return nil, fmt.Errorf("dedifile: not an OKP/Ed25519 JWK (kty=%q crv=%q)", jwk.Kty, jwk.Crv)
	}
	raw, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("dedifile: decode jwk.x: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("dedifile: jwk.x has unexpected length %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// noteKeyPrivate recovers the raw Ed25519 private key from a
// golang.org/x/mod/sumdb/note private key string, the format this node's
// identity key is generated and stored in (see cmd/dedid keygen/nodeKey).
// The encoding is fixed by the note format: "PRIVATE+KEY", the key name, a
// hash, and base64 of one algorithm byte followed by the Ed25519 seed.
func noteKeyPrivate(skey string) (ed25519.PrivateKey, error) {
	parts := strings.SplitN(skey, "+", 5)
	if len(parts) != 5 || parts[0] != "PRIVATE" || parts[1] != "KEY" {
		return nil, fmt.Errorf("dedifile: not a note private key: expected PRIVATE+KEY+<name>+<hash>+<base64>")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, fmt.Errorf("dedifile: note private key is not valid base64: %w", err)
	}
	if len(raw) != 1+ed25519.SeedSize {
		return nil, fmt.Errorf("dedifile: note private key has unexpected length %d", len(raw))
	}
	return ed25519.NewKeyFromSeed(raw[1:]), nil
}

// SignerFromNodeKey derives this node's Ed25519 signing key and a stable kid
// from its note-format identity key (checkpoint.Checkpointer.SKey). One key,
// not two: a relying party that already trusts this node's checkpoint
// signatures needs no separate credential to trust its DeDi files.
//
// The kid is derived from the public key itself (not the note key name),
// so it survives an operator renaming DEDI_ORIGIN without invalidating
// already-cached manifests.
func SignerFromNodeKey(skey string) (priv ed25519.PrivateKey, kid string, err error) {
	priv, err = noteKeyPrivate(skey)
	if err != nil {
		return nil, "", err
	}
	pub := priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	kid = "node-" + hex.EncodeToString(sum[:8])
	return priv, kid, nil
}
