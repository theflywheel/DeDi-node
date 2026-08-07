package delegation

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"
)

var now = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func offerFor(t *testing.T, parent, child string) Offer {
	t.Helper()
	o, err := NewOffer(parent, child, "test child", now)
	if err != nil {
		t.Fatalf("NewOffer(%q, %q): %v", parent, child, err)
	}
	return o
}

func TestAParentCanOnlyDelegateOneLevelBelowItself(t *testing.T) {
	// The check that stops an operator minting an offer for a namespace they
	// do not hold. Without it the parent would sign a record asserting
	// authority it never had — and it would be a perfectly valid log entry.
	for _, tc := range []struct {
		parent, child string
		ok            bool
		why           string
	}{
		{"beckn", "beckn.mobility", true, "the ordinary case"},
		{"beckn", "beckn.retail", true, "another child"},
		{"beckn", "beckn", false, "a namespace is not its own child"},
		{"beckn", "onix", false, "an unrelated namespace"},
		{"beckn", "becknx.mobility", false, "prefix match must not be a string prefix"},
		{"beckn", "beckn.mobility.metro", false, "two levels: metro belongs to mobility to grant"},
		{"beckn", "beckn.", false, "empty leaf"},
		{"beckn", "beckn.Mobility", false, "uppercase would collide case-insensitively downstream"},
		{"beckn", "beckn.mo bility", false, "whitespace"},
		{"", "beckn.mobility", false, "no parent"},
	} {
		err := ValidateChildNamespace(tc.parent, tc.child)
		if tc.ok && err != nil {
			t.Errorf("%s: ValidateChildNamespace(%q, %q) = %v, want nil", tc.why, tc.parent, tc.child, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: ValidateChildNamespace(%q, %q) = nil, want an error", tc.why, tc.parent, tc.child)
		}
	}
}

func TestThePublishedRecordNeverCarriesThePlaintextToken(t *testing.T) {
	// The log is public, replicated to every replica and served
	// unauthenticated. A token in it is the namespace handed to every reader.
	o := offerFor(t, "beckn", "beckn.mobility")
	body, err := json.Marshal(o.Payload())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), o.Token) {
		t.Fatalf("the enrolment token appears in the record published to the log:\n%s", body)
	}
	if o.Payload().TokenHash == "" {
		t.Fatal("no token hash recorded, so no enrolment could ever be checked")
	}
}

// childKey is generated rather than written out as a literal. A hand-made
// verifier key looks right and is not: the hash in the middle is derived from
// the name and the public key, so a fabricated one fails to parse. The first
// version of these tests used one, and it passed only because the check being
// tested was itself too weak to notice.
var childKey = func() string {
	_, vkey, err := note.GenerateKey(rand.Reader, "beckn.mobility/log")
	if err != nil {
		panic(err)
	}
	return vkey
}()

func enrolment(o Offer) Enrolment {
	return Enrolment{
		Namespace: o.Namespace, Token: o.Token,
		Origin: "beckn.mobility/log", URL: "https://mobility.example",
		Key: childKey,
	}
}

func TestRedeemingAnOfferActivatesItAndSpendsTheToken(t *testing.T) {
	o := offerFor(t, "beckn", "beckn.mobility")
	rec, err := Redeem(o.Payload(), enrolment(o), now)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if rec.State != StateActive {
		t.Errorf("state = %q, want %q", rec.State, StateActive)
	}
	if rec.ChildKey == "" || rec.ChildURL == "" || rec.ChildOrigin == "" {
		t.Errorf("child identity not recorded: %+v", rec)
	}
	if rec.TokenHash != "" {
		t.Error("the token hash survived redemption; a spent offer should leave nothing to match against")
	}

	// The second attempt is the one that matters: a leaked token must not let
	// a second claimant take the namespace from the first.
	if _, err := Redeem(rec, enrolment(o), now); !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second redemption: %v, want ErrTokenUsed", err)
	}
}

func TestAWrongTokenIsRefused(t *testing.T) {
	o := offerFor(t, "beckn", "beckn.mobility")
	en := enrolment(o)
	en.Token = "not-the-token"
	if _, err := Redeem(o.Payload(), en, now); !errors.Is(err, ErrBadToken) {
		t.Fatalf("Redeem with a wrong token: %v, want ErrBadToken", err)
	}
}

func TestAnExpiredOfferIsRefused(t *testing.T) {
	o := offerFor(t, "beckn", "beckn.mobility")
	late := now.Add(TokenTTL + time.Second)
	if _, err := Redeem(o.Payload(), enrolment(o), late); !errors.Is(err, ErrTokenStale) {
		t.Fatalf("Redeem after expiry: %v, want ErrTokenStale", err)
	}
	// And it is still refused the moment before, so the boundary is not off by
	// a whole TTL in the permissive direction.
	if _, err := Redeem(o.Payload(), enrolment(o), o.ExpiresAt.Add(-time.Second)); err != nil {
		t.Fatalf("Redeem just before expiry: %v, want success", err)
	}
}

func TestEnrolmentMustCarryAUsableKeyAndURL(t *testing.T) {
	// Both are recorded so that others can verify the child directly. A
	// malformed one is caught here, while the operator is still watching, and
	// not months later when someone first tries to check a proof.
	o := offerFor(t, "beckn", "beckn.mobility")
	for _, tc := range []struct {
		name  string
		mutch func(*Enrolment)
	}{
		{"no key", func(e *Enrolment) { e.Key = "" }},
		{"key is not a note verifier key", func(e *Enrolment) { e.Key = "AAAAAAAA" }},
		{"no origin", func(e *Enrolment) { e.Origin = "" }},
		{"url is not absolute", func(e *Enrolment) { e.URL = "/mobility" }},
		{"url has no host", func(e *Enrolment) { e.URL = "https://" }},
		{"url is not http", func(e *Enrolment) { e.URL = "ftp://mobility.example" }},
	} {
		en := enrolment(o)
		tc.mutch(&en)
		if _, err := Redeem(o.Payload(), en, now); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: %v, want ErrBadRequest", tc.name, err)
		}
	}
}

func TestAKeyWhoseBase64ContainsAPlusIsAccepted(t *testing.T) {
	// The bug this exists for: the key format was checked by counting "+"
	// separators, and a note verifier key is name+hash+base64 — so two looked
	// right. But the base64 payload contains + roughly a third of the time, and
	// those keys were rejected as malformed.
	//
	// This is the exact key a real child generated when the E2E run first
	// failed. Counting separators gives three.
	const key = "beckn.mobility/log+0a559fae+ATK91hgxP7WqWdpzmDO/B+/WcNZNku7dx9lYiCAjhnNe"
	if strings.Count(key, "+") != 3 {
		t.Fatalf("the regression key no longer has the property under test")
	}
	o := offerFor(t, "beckn", "beckn.mobility")
	en := enrolment(o)
	en.Key = key
	if _, err := Redeem(o.Payload(), en, now); err != nil {
		t.Fatalf("a valid verifier key was refused: %v", err)
	}
}

func TestAKeyThatIsNotAVerifierKeyIsStillRefused(t *testing.T) {
	// The looser check must not have become no check at all.
	o := offerFor(t, "beckn", "beckn.mobility")
	for _, bad := range []string{"AAAA", "name+deadbeef+not-base64!!", "name++", "+ + +"} {
		en := enrolment(o)
		en.Key = bad
		if _, err := Redeem(o.Payload(), en, now); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%q was accepted as a verifier key: %v", bad, err)
		}
	}
}

func TestTokensDifferBetweenOffers(t *testing.T) {
	// A predictable token is a delegation anyone can claim.
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		o := offerFor(t, "beckn", "beckn.mobility")
		if seen[o.Token] {
			t.Fatalf("token repeated after %d offers", i)
		}
		seen[o.Token] = true
	}
}

func TestParentOfStripsExactlyOneLevel(t *testing.T) {
	if got := parentOf("beckn.mobility"); got != "beckn" {
		t.Errorf("parentOf = %q, want beckn", got)
	}
	if got := parentOf("beckn"); got != "beckn" {
		t.Errorf("parentOf of a root = %q, want it unchanged", got)
	}
}
