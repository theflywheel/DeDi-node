// Package delegation issues and redeems authority over a child namespace.
//
// A child node is a *separate* DeDi node — its own identity key, its own
// database, its own log — whose authority over a sub-namespace this node
// vouches for. That is a different relationship from both of the ones the node
// already models, and the difference is the whole point:
//
//   - A Raft replica is this node, duplicated. Same key, same log, no new
//     trust boundary. It exists so the directory stays up.
//   - A witness peer is a stranger. No shared authority at all; it exists to
//     prove this node has not rewritten history.
//   - A child node is neither. It has its own key and its own history, so it
//     can be verified independently — but it holds a slice of *this* node's
//     namespace, granted by a record in this node's log.
//
// # The parent never holds the child's private key
//
// The obvious implementation is for the parent to generate the child's identity
// keypair and hand the whole bundle over. It is one click and it is wrong. A
// parent that has held the child's private key can forge the child's signed
// checkpoints indefinitely, which means the child's log proves nothing that the
// parent's word did not already assert, and "independently verifiable child"
// becomes decoration.
//
// So enrolment is two-step. The parent mints an *offer*: a namespace, a scope,
// and a one-time secret. The child generates its own key on first boot (the
// same self-provisioning path an unparented node uses) and presents its public
// key along with the secret. Only then does the parent publish the delegation.
// The private key never crosses the boundary, and the parent's log records only
// what is safe to publish.
//
// # Delegation state lives in the log, not in a side table
//
// Every fact here — the offer, the enrolment, a later revocation — is appended
// to the parent's own transparency log rather than kept in a table beside it.
// That is not tidiness. A delegation is exactly the kind of claim the log
// exists to make checkable: a relying party can ask "when was this child
// granted this namespace, and by whom?" and get an answer backed by an
// inclusion proof, against a signed checkpoint witnesses have countersigned.
// A side table would answer the same question with the operator's word.
package delegation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/mod/sumdb/note"
)

// Registry is the reserved registry, inside the parent's own namespace, that
// holds one record per child. Reserved names start with an underscore, which no
// Beckn registry name uses, so a delegation record cannot collide with a real
// one.
const Registry = "_delegations"

// TokenTTL bounds how long an unredeemed offer stays usable.
//
// An enrolment secret is a bearer credential: whoever holds it can claim the
// namespace. Bounding its life bounds the damage from one leaking into a chat
// log or a CI transcript, which is where deploy secrets actually leak. An hour
// is long enough to paste config into a host and boot it, and short enough that
// a stale offer in a terminal buffer is worthless by morning.
const TokenTTL = time.Hour

var (
	ErrBadToken    = errors.New("enrolment token does not match")
	ErrTokenUsed   = errors.New("this offer has already been redeemed")
	ErrTokenStale  = errors.New("enrolment offer has expired")
	ErrBadRequest  = errors.New("invalid delegation request")
	ErrNotDelegate = errors.New("no delegation offer for that namespace")
)

// States a delegation record moves through. It only ever moves forward, and
// every transition is a new version of the same record, so the history of a
// delegation is readable from the log.
const (
	StateOffered = "offered" // minted, not yet claimed
	StateActive  = "active"  // child enrolled and serving
	StateRevoked = "revoked" // withdrawn by the parent
)

// Offer is a minted, unredeemed delegation. The plaintext token exists only in
// the response that mints it — see Record, which stores the hash.
type Offer struct {
	Namespace string    `json:"namespace"`
	Label     string    `json:"label"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Record is the payload published to the parent's log. It is deliberately
// free of anything secret: the log is public, replicated to every replica, and
// served to anyone who asks.
type Record struct {
	State     string `json:"state"`
	Namespace string `json:"namespace"`
	Label     string `json:"label,omitempty"`

	// TokenHash, not the token. A public log that carried the enrolment secret
	// would hand the namespace to every reader, and this log is served
	// unauthenticated by design.
	TokenHash string `json:"token_hash,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`

	// Filled in at enrolment. ChildKey is what makes the child independently
	// verifiable — with it a browser can check the child's checkpoint
	// signatures without asking the parent to vouch for anything.
	ChildOrigin string `json:"child_origin,omitempty"`
	ChildKey    string `json:"child_key,omitempty"`
	ChildURL    string `json:"child_url,omitempty"`
	EnrolledAt  string `json:"enrolled_at,omitempty"`

	RevokedAt string `json:"revoked_at,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Enrolment is what a child presents to claim its offer.
type Enrolment struct {
	Namespace string `json:"namespace"`
	Token     string `json:"token"`
	Origin    string `json:"origin"`
	Key       string `json:"key"`
	URL       string `json:"url"`
}

// NewOffer mints an offer for a child namespace.
//
// now is passed in rather than read from the clock because the expiry ends up
// inside a log entry, and every value that reaches a leaf has to be decided
// once by the proposer rather than re-read on each replica — the same rule
// store.AppendInput.CreatedAt exists to enforce.
func NewOffer(parentNS, childNS, label string, now time.Time) (Offer, error) {
	if err := ValidateChildNamespace(parentNS, childNS); err != nil {
		return Offer{}, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Offer{}, fmt.Errorf("minting enrolment token: %w", err)
	}
	return Offer{
		Namespace: childNS,
		Label:     strings.TrimSpace(label),
		Token:     base64.RawURLEncoding.EncodeToString(buf),
		ExpiresAt: now.Add(TokenTTL).UTC().Truncate(time.Second),
	}, nil
}

// HashToken is the one-way function standing between the public log and the
// enrolment secret.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Payload renders the offer as the record body to append.
func (o Offer) Payload() Record {
	return Record{
		State:     StateOffered,
		Namespace: o.Namespace,
		Label:     o.Label,
		TokenHash: HashToken(o.Token),
		ExpiresAt: o.ExpiresAt.Format(time.RFC3339),
	}
}

// ValidateChildNamespace enforces that a child namespace is genuinely *under*
// the parent's.
//
// A parent can only delegate what it holds. Without this check an operator
// could mint an offer for any namespace at all — including one another node
// already serves — and publish a signed record asserting authority it never
// had. The log would faithfully record the overreach, which is better than
// nothing, but the right place to stop it is before it is signed.
func ValidateChildNamespace(parent, child string) error {
	parent, child = strings.TrimSpace(parent), strings.TrimSpace(child)
	if parent == "" || child == "" {
		return fmt.Errorf("%w: parent and child namespace are both required", ErrBadRequest)
	}
	if !strings.HasPrefix(child, parent+".") {
		return fmt.Errorf("%w: child namespace %q must sit under %q, as %q",
			ErrBadRequest, child, parent, parent+".<child>")
	}
	leaf := strings.TrimPrefix(child, parent+".")
	if leaf == "" || strings.Contains(leaf, ".") {
		return fmt.Errorf("%w: %q must delegate exactly one level below %q",
			ErrBadRequest, child, parent)
	}
	for _, r := range leaf {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("%w: %q may contain only lowercase letters, digits, - and _",
				ErrBadRequest, leaf)
		}
	}
	return nil
}

// Redeem checks an enrolment against the offer as recorded in the log and
// returns the record to append in its place.
//
// The comparison is on the hash, so a reader of the log — including every
// replica, and every witness that mirrors it — learns nothing that would let
// them claim the namespace themselves.
func Redeem(rec Record, en Enrolment, now time.Time) (Record, error) {
	switch rec.State {
	case StateOffered: // the only state that can be redeemed
	case StateActive:
		return Record{}, ErrTokenUsed
	default:
		return Record{}, fmt.Errorf("%w: delegation is %s", ErrNotDelegate, rec.State)
	}
	if rec.TokenHash == "" || HashToken(en.Token) != rec.TokenHash {
		return Record{}, ErrBadToken
	}
	if exp, err := time.Parse(time.RFC3339, rec.ExpiresAt); err == nil && now.After(exp) {
		return Record{}, ErrTokenStale
	}
	if err := validateEnrolment(en); err != nil {
		return Record{}, err
	}

	out := rec
	out.State = StateActive
	out.ChildOrigin = en.Origin
	out.ChildKey = en.Key
	out.ChildURL = strings.TrimRight(en.URL, "/")
	out.EnrolledAt = now.UTC().Format(time.RFC3339)
	// The offer is spent. Clearing the hash makes that explicit in the log
	// rather than leaving a redeemed secret's fingerprint lying around, and
	// makes a replay attempt fail on the state check above rather than on a
	// hash comparison that would otherwise still succeed.
	out.TokenHash = ""
	out.ExpiresAt = ""
	return out, nil
}

func validateEnrolment(en Enrolment) error {
	if strings.TrimSpace(en.Origin) == "" {
		return fmt.Errorf("%w: origin is required", ErrBadRequest)
	}
	if strings.TrimSpace(en.Key) == "" {
		return fmt.Errorf("%w: key is required", ErrBadRequest)
	}
	// Parsed with the same library that will later verify the child's
	// checkpoints, rather than pattern-matched.
	//
	// The first version of this counted separators — a note verifier key is
	// name+hash+base64, so "exactly two +" looked sufficient. It is not: the
	// base64 payload contains + about a third of the time, and the check
	// rejected perfectly good keys. It was caught by running a real child
	// against a real parent, and no amount of staring at the rule would have
	// found it, because the rule reads correct.
	if _, err := note.NewVerifier(strings.TrimSpace(en.Key)); err != nil {
		return fmt.Errorf("%w: key must be a sumdb/note verifier key (name+hash+base64): %v", ErrBadRequest, err)
	}
	u, err := url.Parse(strings.TrimSpace(en.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: url must be an absolute http(s) URL", ErrBadRequest)
	}
	return nil
}

// ParseRecord reads a delegation payload out of a stored entry.
func ParseRecord(raw []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	return rec, nil
}
