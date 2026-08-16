package domainproof

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// zone is a stub resolver: a fixed map from name to TXT records.
type zone map[string][]string

func (z zone) LookupTXT(_ context.Context, name string) ([]string, error) {
	txt, ok := z[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return txt, nil
}

const nodeKey = "node-verifier-key-aaa"

func TestChallengeIsPublishableAndStable(t *testing.T) {
	name, value := Challenge("beckn-testnet", "example.org", nodeKey)
	if name != "_dedi-challenge.example.org" {
		t.Fatalf("challenge name = %q", name)
	}
	if !strings.HasPrefix(value, "dedi-verification=") {
		t.Fatalf("challenge value = %q", value)
	}
	// Derived, not issued: asking twice must give the same answer, or an
	// operator who publishes the record and comes back tomorrow finds it no
	// longer counts.
	name2, value2 := Challenge("beckn-testnet", "example.org", nodeKey)
	if name != name2 || value != value2 {
		t.Fatal("Challenge is not deterministic")
	}
	// A trailing dot and different casing name the same zone, so they must not
	// produce two different bindings.
	if n, v := Challenge("beckn-testnet", "Example.ORG.", nodeKey); n != name || v != value {
		t.Fatalf("normalization failed: %q %q", n, v)
	}
}

// TestChallengeDoesNotTransfer is the security property. A TXT record is a
// domain owner consenting to one specific binding; if the same token worked for
// another namespace or another node, publishing it once would authorize every
// future claim anyone cared to make.
func TestChallengeDoesNotTransfer(t *testing.T) {
	_, base := Challenge("ns-a", "example.org", nodeKey)
	cases := map[string]string{
		"another namespace": mustValue(Challenge("ns-b", "example.org", nodeKey)),
		"another domain":    mustValue(Challenge("ns-a", "other.org", nodeKey)),
		"another node":      mustValue(Challenge("ns-a", "example.org", "different-node-key")),
	}
	for what, got := range cases {
		if got == base {
			t.Errorf("token for %s is identical to the original — it transfers", what)
		}
	}
}

func TestVerifyAcceptsThePublishedRecordAndNothingElse(t *testing.T) {
	name, value := Challenge("beckn-testnet", "example.org", nodeKey)
	ctx := context.Background()

	t.Run("published", func(t *testing.T) {
		z := zone{name: {"v=spf1 -all", value}} // alongside unrelated TXT records
		if err := Verify(ctx, z, "beckn-testnet", "example.org", nodeKey); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("not published", func(t *testing.T) {
		err := Verify(ctx, zone{}, "beckn-testnet", "example.org", nodeKey)
		if !errors.Is(err, ErrNoMatchingRecord) {
			t.Fatalf("want ErrNoMatchingRecord, got %v", err)
		}
	})

	t.Run("someone else's token", func(t *testing.T) {
		_, other := Challenge("someone-else", "example.org", nodeKey)
		err := Verify(ctx, zone{name: {other}}, "beckn-testnet", "example.org", nodeKey)
		if !errors.Is(err, ErrNoMatchingRecord) {
			t.Fatalf("a token issued for another namespace verified this one: %v", err)
		}
	})

	t.Run("no domain declared", func(t *testing.T) {
		if err := Verify(ctx, zone{}, "beckn-testnet", "  ", nodeKey); err == nil {
			t.Fatal("verifying an empty domain should fail")
		}
	})
}

// TestVerifyDistinguishesNotPublishedFromResolverFailure keeps the two apart at
// the type level, because the handler reports one as the caller's state (400)
// and the other as an upstream fault (502). Collapsing them would tell an
// operator their DNS is wrong when in fact the node cannot resolve anything.
func TestVerifyDistinguishesNotPublishedFromResolverFailure(t *testing.T) {
	err := Verify(context.Background(), brokenResolver{}, "ns", "example.org", nodeKey)
	if errors.Is(err, ErrNoMatchingRecord) {
		t.Fatalf("a broken resolver was reported as a missing record: %v", err)
	}
	if err == nil {
		t.Fatal("a broken resolver should fail")
	}
}

type brokenResolver struct{}

func (brokenResolver) LookupTXT(context.Context, string) ([]string, error) {
	return nil, errors.New("connection refused")
}

func mustValue(_, v string) string { return v }
