// Package publisher authenticates writes to the publisher plane (design.md
// §4.4, §5.3). There are no sessions and no bearer tokens: every write is an
// Ed25519-signed request, and the signing key id is recorded on the version it
// produces, so the log itself names the key behind each governance decision.
package publisher

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Signature scheme version. It is the first line of every signed preimage, so a
// signature made under one scheme can never be replayed under another.
const scheme = "dedi/v1/publish"

// Wire headers carrying the signature.
const (
	HeaderKeyID     = "DeDi-Key-Id"
	HeaderTimestamp = "DeDi-Timestamp"
	HeaderSignature = "DeDi-Signature"
)

// DefaultMaxSkew bounds how far a request timestamp may be from the server
// clock in either direction. It bounds replay of a captured request, it does
// not prevent it: within the window an identical request verifies again. That
// is closed by publish idempotency (expected-version on the append), not here.
const DefaultMaxSkew = 5 * time.Minute

var (
	ErrNoSignature   = errors.New("request is not signed")
	ErrUnknownKey    = errors.New("unknown publisher key")
	ErrBadSignature  = errors.New("signature does not verify")
	ErrStale         = errors.New("request timestamp outside the accepted window")
	ErrWrongScope    = errors.New("publisher key is not scoped to this namespace")
	ErrMalformedKey  = errors.New("malformed publisher key")
	ErrMalformedHead = errors.New("malformed signature header")
)

// Key is a publisher credential: an Ed25519 public key, its id, and the
// namespace it may write to. Scoping is per namespace by design — a grant for
// one network is never an implicit grant over another (governance.md).
type Key struct {
	KID       string
	Namespace string
	Public    ed25519.PublicKey
}

// KeySet resolves key ids to keys. Today it is loaded from operator config at
// boot; the <ns>/_keys registry (design.md §4.4, recursive trust) becomes an
// additional source once the publisher plane can write it.
type KeySet struct {
	keys map[string]Key
}

// ParseKeySet reads "kid:namespace:base64pubkey" entries separated by commas or
// whitespace, the form used by DEDI_PUBLISHER_KEYS. An empty spec yields an
// empty set, which authenticates nothing — writes stay closed by default.
func ParseKeySet(spec string) (*KeySet, error) {
	ks := &KeySet{keys: map[string]Key{}}
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	}) {
		parts := strings.Split(entry, ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("%w: want kid:namespace:base64pubkey, got %q", ErrMalformedKey, entry)
		}
		raw, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrMalformedKey, parts[0], err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: %q: public key is %d bytes, want %d",
				ErrMalformedKey, parts[0], len(raw), ed25519.PublicKeySize)
		}
		if _, dup := ks.keys[parts[0]]; dup {
			return nil, fmt.Errorf("%w: duplicate key id %q", ErrMalformedKey, parts[0])
		}
		ks.keys[parts[0]] = Key{KID: parts[0], Namespace: parts[1], Public: ed25519.PublicKey(raw)}
	}
	return ks, nil
}

// Len reports how many keys are loaded. Zero means the write plane is shut.
func (ks *KeySet) Len() int { return len(ks.keys) }

// Lookup resolves a key id.
func (ks *KeySet) Lookup(kid string) (Key, bool) {
	k, ok := ks.keys[kid]
	return k, ok
}

// Preimage is the exact byte string a publisher signs. Binding the method, the
// path and a digest of the body means a captured signature cannot be moved to a
// different route or reused with different content; binding the timestamp bounds
// how long it stays usable at all.
//
// Field order and separator are part of the wire contract — changing either
// invalidates every existing signature.
func Preimage(method, path string, body []byte, ts time.Time) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		scheme,
		strings.ToUpper(method),
		path,
		base64.StdEncoding.EncodeToString(sum[:]),
		ts.UTC().Format(time.RFC3339),
	}, "\n"))
}

// Sign produces the DeDi-Signature value for a request. Used by operator
// tooling and by the tests; the server only ever verifies.
func Sign(priv ed25519.PrivateKey, method, path string, body []byte, ts time.Time) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, Preimage(method, path, body, ts)))
}

// Verify authenticates a signed request against the key set. It returns the key
// that signed it, so the caller can scope the write and record the key id on the
// resulting version.
//
// now is passed in rather than read from the clock to keep the skew check
// testable.
func (ks *KeySet) Verify(method, path string, body []byte, kid, tsHeader, sigHeader string, now time.Time, maxSkew time.Duration) (Key, error) {
	if kid == "" && tsHeader == "" && sigHeader == "" {
		return Key{}, ErrNoSignature
	}
	if kid == "" || tsHeader == "" || sigHeader == "" {
		return Key{}, fmt.Errorf("%w: %s, %s and %s are all required",
			ErrMalformedHead, HeaderKeyID, HeaderTimestamp, HeaderSignature)
	}
	ts, err := time.Parse(time.RFC3339, tsHeader)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %s must be RFC 3339", ErrMalformedHead, HeaderTimestamp)
	}
	if d := now.Sub(ts); d > maxSkew || d < -maxSkew {
		return Key{}, ErrStale
	}
	sig, err := base64.StdEncoding.DecodeString(sigHeader)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %s must be standard base64", ErrMalformedHead, HeaderSignature)
	}
	// Resolve the key before verifying, but report an unknown key id and a bad
	// signature as distinct errors: which key ids exist is not a secret, and
	// collapsing them makes operator debugging needlessly painful.
	key, ok := ks.Lookup(kid)
	if !ok {
		return Key{}, ErrUnknownKey
	}
	if !ed25519.Verify(key.Public, Preimage(method, path, body, ts), sig) {
		return Key{}, ErrBadSignature
	}
	return key, nil
}

// Authorizes reports whether the key may write to the given namespace.
func (k Key) Authorizes(namespace string) error {
	if k.Namespace != namespace {
		return fmt.Errorf("%w: key %q is scoped to %q, not %q", ErrWrongScope, k.KID, k.Namespace, namespace)
	}
	return nil
}
