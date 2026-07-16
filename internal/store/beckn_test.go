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

	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-abc")
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
	e, err := s.FindBecknSubscriber(context.Background(), "bap.example.com", "key-dup")
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

	if _, err := s.FindBecknSubscriber(context.Background(), "gone.example.com", "key-rev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked latest version should be invisible, got %v", err)
	}
	if _, err := s.FindBecknSubscriber(context.Background(), "nobody.example.com", "no-such-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("miss should be ErrNotFound, got %v", err)
	}
}
