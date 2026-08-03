package store

import (
	"context"
	"errors"
	"testing"
)

func TestFindBecknSubscriberByPayloadSubscriberID(t *testing.T) {
	s := testStore(t)
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-abc",
		PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","signing_public_key":"k1"}`), CreatedBy: "t"})

	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-abc", nil)
	if err != nil {
		t.Fatalf("find by subscriber_id: %v", err)
	}
	if e.Namespace != "beckn-testnet" || e.RecordName != "key-abc" {
		t.Fatalf("wrong entry: %+v", e)
	}
}

func TestFindBecknSubscriberPrefersNamespaceMatch(t *testing.T) {
	s := testStore(t)
	for _, ns := range []string{"ns-a", "bap.example.com"} {
		mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(`{}`), CreatedBy: "t"})
		mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: ns, Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
		mustAppend(t, s, AppendInput{EntryType: "record", Namespace: ns, Registry: "subscribers.beckn.one", RecordName: "key-dup",
			PayloadRaw: []byte(`{"subscriber_id":"bap.example.com","from":"` + ns + `"}`), CreatedBy: "t"})
	}
	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-dup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Namespace != "bap.example.com" {
		t.Fatalf("namespace match not preferred, got ns %q", e.Namespace)
	}
}

func TestFindBecknSubscriberSkipsNonLiveAndMisses(t *testing.T) {
	s := testStore(t)
	seedNSReg(t, s)
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "key-rev",
		PayloadRaw: []byte(`{"subscriber_id":"gone.example.com"}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "key-rev",
		PayloadRaw: []byte(`{"subscriber_id":"gone.example.com","status":"revoked"}`), State: "revoked", CreatedBy: "t"})

	if _, err := s.FindBecknSubscriber(context.Background(), "gone.example.com", "key-rev", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked latest version should be invisible, got %v", err)
	}
	if _, err := s.FindBecknSubscriber(context.Background(), "nobody.example.com", "no-such-key", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("miss should be ErrNotFound, got %v", err)
	}
}

// The escalation the eligibility constraint exists to stop (design.md:256):
// a record named {key_id} published in an unrelated namespace must not be able
// to answer a wildcard lookup for someone else's subscriber_id.
func TestFindBecknSubscriberEligibilityBlocksForeignNamespace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// The legitimate participant.
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-x",
		PayloadRaw: []byte(`{"subscriber_id":"bpp.example.com","signing_public_key":"legit"}`), CreatedBy: "t"})
	// A rogue namespace claiming the same subscriber_id under the same key id.
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "rogue-net", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "rogue-net", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "rogue-net", Registry: "subscribers.beckn.one", RecordName: "key-y",
		PayloadRaw: []byte(`{"subscriber_id":"bpp.example.com","signing_public_key":"attacker"}`), CreatedBy: "t"})

	// Unrestricted: the rogue record answers, which is exactly the hole.
	e, err := s.FindBecknSubscriber(ctx, "bpp.example.com", "key-y", nil)
	if err != nil {
		t.Fatalf("unrestricted lookup: %v", err)
	}
	if e.Namespace != "rogue-net" {
		t.Fatalf("expected the rogue record to answer when unrestricted, got %q", e.Namespace)
	}

	// Restricted to the real network: the rogue record is invisible.
	eligible := []string{"beckn-testnet"}
	if _, err := s.FindBecknSubscriber(ctx, "bpp.example.com", "key-y", eligible); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rogue namespace answered a wildcard lookup despite the allowlist: %v", err)
	}
	// ...while the legitimate participant still resolves.
	got, err := s.FindBecknSubscriber(ctx, "bpp.example.com", "key-x", eligible)
	if err != nil {
		t.Fatalf("eligible namespace stopped resolving: %v", err)
	}
	if got.Namespace != "beckn-testnet" {
		t.Fatalf("wrong namespace: %q", got.Namespace)
	}
}

// An explicitly empty allowlist is "nothing is eligible", not "everything".
func TestFindBecknSubscriberEmptyAllowlistResolvesNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "beckn-testnet", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "beckn-testnet", Registry: "subscribers.beckn.one", RecordName: "key-z",
		PayloadRaw: []byte(`{"subscriber_id":"bpp.example.com"}`), CreatedBy: "t"})

	if _, err := s.FindBecknSubscriber(ctx, "bpp.example.com", "key-z", []string{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty allowlist resolved a record: %v", err)
	}
}
