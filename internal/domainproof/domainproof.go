// Package domainproof binds a namespace to the DNS domain it claims, by
// asking the domain's operator to publish a TXT record only they can publish.
//
// The gap it closes (docs/spec-gaps.md G8): a namespace carries a `domain`
// field, and until now nothing checked that the node serving the namespace had
// any relationship to that domain. A reader following `?domain=` discovery
// (internal/store/discovery.go) was trusting an assertion the publisher made
// about itself.
//
// Namespace and domain stay separate identifiers — a namespace may name a
// sector or a community while the domain is a link to whoever runs it, and the
// DeDi schema only says a namespace is "usually the domain," never that it must
// be. This package does not collapse them; it makes the edge between them
// checkable.
package domainproof

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Prefix is the label the challenge TXT record is published under, so the
// proof lives at a dedicated name instead of competing for the domain's apex
// TXT records with SPF, DMARC and everything else that already lives there.
const Prefix = "_dedi-challenge"

// tokenPrefix is the value's fixed leading marker. A domain may carry many TXT
// records at this name over time — a re-verification after a key rotation
// leaves the old one behind — so the check scans for a matching value rather
// than requiring the set to hold exactly one.
const tokenPrefix = "dedi-verification="

// domainSeparator is the version tag mixed into the token. It is part of the
// hashed input, so a future scheme change produces entirely different tokens
// rather than tokens that a v1 verifier might accidentally accept.
const domainSeparator = "dedi-domain-verification-v1"

// ErrNoMatchingRecord means the domain answered, but none of its TXT records
// at the challenge name carried this namespace's token.
var ErrNoMatchingRecord = errors.New("no matching dedi-verification TXT record")

// Resolver is the DNS lookup this package needs. *net.Resolver satisfies it;
// tests supply their own rather than depending on the public DNS.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Challenge returns the TXT record an operator must publish to bind namespace
// to domain on the node identified by nodeKey.
//
// The token is derived rather than issued, which is what keeps this stateless:
// there is no pending-challenge table to expire, reap, or lose in a failover,
// and a node that restarts mid-verification asks for the same record it asked
// for before. It is a hash of all three inputs, so a TXT record proving one
// namespace does not prove another, and one published for a different node
// does not transfer to this one — a domain owner is consenting to a specific
// binding, not signing a blank cheque.
//
// Nothing here is secret. The token's job is to prove control of the domain's
// DNS zone, not to be unguessable: an attacker who can compute the token still
// cannot publish it under someone else's domain, and one who can publish under
// that domain has already won.
func Challenge(namespace, domain, nodeKey string) (name, value string) {
	sum := sha256.Sum256([]byte(domainSeparator + "|" + namespace + "|" + normalize(domain) + "|" + nodeKey))
	token := base64.RawURLEncoding.EncodeToString(sum[:])
	return Prefix + "." + normalize(domain), tokenPrefix + token
}

// Verify resolves the challenge name and reports whether the domain currently
// publishes this namespace's token.
//
// It is deliberately a live read with no caching: a verdict is recorded in the
// log with the time it was taken (see internal/api/domain.go), so a stale
// answer here would be written down as a fact.
func Verify(ctx context.Context, res Resolver, namespace, domain, nodeKey string) error {
	if strings.TrimSpace(domain) == "" {
		return fmt.Errorf("namespace %q declares no domain to verify", namespace)
	}
	name, want := Challenge(namespace, domain, nodeKey)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	txt, err := res.LookupTXT(ctx, name)
	if err != nil {
		// A missing name is the ordinary "you have not published it yet" case
		// and must not read as a node fault. Distinguishing it here means the
		// handler can answer 400 for that and 502 for a resolver that is
		// genuinely broken.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && (dnsErr.IsNotFound || dnsErr.IsTemporary) {
			return fmt.Errorf("%w: %s has no TXT records (%v)", ErrNoMatchingRecord, name, err)
		}
		return fmt.Errorf("resolving %s: %w", name, err)
	}
	for _, got := range txt {
		// Constant-time comparison would be theatre: the expected value is
		// public and derivable by anyone, so there is no secret to leak.
		if strings.TrimSpace(got) == want {
			return nil
		}
	}
	return fmt.Errorf("%w at %s: found %d record(s), none matching %s", ErrNoMatchingRecord, name, len(txt), want)
}

// normalize lower-cases the domain and drops a trailing dot, so a namespace
// declaring "Example.ORG." and one declaring "example.org" produce the same
// challenge instead of two bindings for one zone.
func normalize(domain string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
}
