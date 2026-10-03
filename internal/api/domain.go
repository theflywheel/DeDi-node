package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/domainproof"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// Where domain verdicts are recorded. An underscore namespace, so it is this
// node's bookkeeping rather than published directory data: internal_ns.go hides
// it from the spec read endpoints and internal/dedifile skips it when building
// published files.
//
// Recording the verdict as a log entry rather than as a column is the point.
// It inherits everything the log already gives us — it replicates through Raft
// with every other write, it lands in the Merkle tree so a witness covers it,
// and its history is queryable, so "when did this stop being verified, and who
// unverified it" has an answer.
const (
	domainProofNS       = "_domains"
	domainProofRegistry = "verified"
)

// domainProofRecord is one recorded verdict.
type domainProofRecord struct {
	Namespace  string `json:"namespace"`
	Domain     string `json:"domain"`
	Method     string `json:"method"`
	TXTName    string `json:"txt_name"`
	VerifiedAt string `json:"verified_at"`
	NodeKey    string `json:"node_key"`
}

// domainStatus is what the console and the operator see.
type domainStatus struct {
	Namespace string `json:"namespace"`
	Domain    string `json:"domain"`
	Verified  bool   `json:"verified"`
	// VerifiedAt and VerifiedFor are set only when Verified. VerifiedFor is
	// the domain the proof was taken against, which is not always Domain: a
	// namespace whose declared domain has since been edited has a verdict that
	// no longer applies, and showing both is how that is visible rather than
	// silently inherited.
	VerifiedAt  string `json:"verified_at,omitempty"`
	VerifiedFor string `json:"verified_for,omitempty"`
	// The record to publish, always included — an operator re-reads this to
	// re-verify after rotating the node key, and a verified namespace whose
	// challenge has changed still needs to be told what the new one is.
	Challenge domainChallengeDTO `json:"challenge"`
}

type domainChallengeDTO struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// resolver is the DNS lookup used by verifyDomain. A field so tests can supply
// their own zone instead of depending on the public DNS, and nil means the
// system resolver.
func (s *Server) resolver() domainproof.Resolver {
	if s.DNSResolver != nil {
		return s.DNSResolver
	}
	return net.DefaultResolver
}

// namespaceDomain reads the domain a namespace declares, or "" if it declares
// none. The namespace must exist; a missing one is reported so the handler can
// answer 404 rather than offering a challenge for something we do not serve.
func (s *Server) namespaceDomain(ctx context.Context, ns string) (string, error) {
	e, err := s.Store.Resolve(ctx, "namespace", ns, "", "", nil, nil)
	if err != nil {
		return "", err
	}
	return parseMeta(e.PayloadRaw).Domain, nil
}

// domainProof returns the recorded verdict for a namespace, if any. A verdict
// stored with state "revoked" (see unverifyDomain) reads as no verdict, which
// is what makes withdrawing one possible in an append-only log.
func (s *Server) domainProof(ctx context.Context, ns string) (domainProofRecord, bool) {
	e, err := s.Store.Resolve(ctx, "record", domainProofNS, domainProofRegistry, ns, nil, nil)
	if err != nil || e.State == "revoked" {
		return domainProofRecord{}, false
	}
	var rec domainProofRecord
	if json.Unmarshal(e.PayloadRaw, &rec) != nil {
		return domainProofRecord{}, false
	}
	return rec, true
}

// domainStatusFor assembles the current picture for one namespace.
func (s *Server) domainStatusFor(ctx context.Context, ns string) (domainStatus, error) {
	domain, err := s.namespaceDomain(ctx, ns)
	if err != nil {
		return domainStatus{}, err
	}
	name, value := domainproof.Challenge(ns, domain, s.VerifierKey)
	out := domainStatus{
		Namespace: ns,
		Domain:    domain,
		Challenge: domainChallengeDTO{Type: "TXT", Name: name, Value: value},
	}
	if rec, ok := s.domainProof(ctx, ns); ok {
		out.Verified, out.VerifiedAt, out.VerifiedFor = true, rec.VerifiedAt, rec.Domain
	}
	return out, nil
}

// showDomain answers "what do I publish, and where do I stand" for one
// namespace. Deliberately on the write plane rather than the read plane: the
// verdict is this node's own bookkeeping, and putting it on /dedi/lookup would
// add a field the standard's response schema does not declare.
func (s *Server) showDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := scoped(w, r); !ok {
		return
	}
	ns := r.PathValue("namespace")
	status, err := s.domainStatusFor(r.Context(), ns)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "namespace")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Message: "Domain verification status retrieved successfully", Data: status})
}

// verifyDomain resolves the challenge and, if it is published, records the
// binding.
func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	// A follower's replica may trail the leader, so it must not decide this
	// write from its own state: a namespace or proof it has not applied yet
	// would read as missing, a 404 or a 400 for a request the leader accepts.
	// Same rule as unchanged() and revoke; see onFollower.
	if s.onFollower() {
		s.redirectToLeader(w, r)
		return
	}
	ns := r.PathValue("namespace")
	domain, err := s.namespaceDomain(r.Context(), ns)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "namespace")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if domain == "" {
		badRequest(w, "namespace "+ns+" declares no domain; publish one before verifying it")
		return
	}
	if err := domainproof.Verify(r.Context(), s.resolver(), ns, domain, s.VerifierKey); err != nil {
		// "you have not published it yet" is the caller's state, not a node
		// fault; anything else is the resolver failing and must not be
		// reported as though the operator got it wrong.
		if errors.Is(err, domainproof.ErrNoMatchingRecord) {
			badRequest(w, err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, "UPSTREAM_ERROR", err.Error())
		return
	}
	if err := s.ensureDomainProofParents(r.Context(), key); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	name, _ := domainproof.Challenge(ns, domain, s.VerifierKey)
	payload, err := json.Marshal(domainProofRecord{
		Namespace: ns, Domain: domain, Method: "dns-txt", TXTName: name,
		VerifiedAt: time.Now().UTC().Format(time.RFC3339), NodeKey: s.VerifierKey,
	})
	if err != nil {
		internal(w, err)
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "record", Namespace: domainProofNS, Registry: domainProofRegistry,
		RecordName: ns, PayloadRaw: payload,
	}, "Domain verified successfully")
}

// unverifyDomain withdraws a binding.
//
// It exists because verification is a claim about the present, and a domain
// that changes hands takes its TXT record with it. Without this the only way
// to retract would be to edit history, which an append-only log will not do —
// so retraction is a new entry carrying state "revoked", exactly as record
// revocation works everywhere else in this node.
func (s *Server) unverifyDomain(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	// A follower's replica may trail the leader, so it must not decide this
	// write from its own state: a namespace or proof it has not applied yet
	// would read as missing, a 404 or a 400 for a request the leader accepts.
	// Same rule as unchanged() and revoke; see onFollower.
	if s.onFollower() {
		s.redirectToLeader(w, r)
		return
	}
	ns := r.PathValue("namespace")
	rec, ok := s.domainProof(r.Context(), ns)
	if !ok {
		notFound(w, "domain verification")
		return
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		internal(w, err)
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "record", Namespace: domainProofNS, Registry: domainProofRegistry,
		RecordName: ns, PayloadRaw: payload, State: "revoked",
	}, "Domain verification withdrawn")
}

// ensureDomainProofParents creates the bookkeeping namespace and registry on
// first use, the same way internal/witness does for `_witness`.
func (s *Server) ensureDomainProofParents(ctx context.Context, key publisher.Key) error {
	create := func(in store.AppendInput) error {
		in.CreatedBy = "publisher:" + key.KID
		_, err := s.writer().Append(ctx, in)
		return err
	}
	if _, err := s.Store.Resolve(ctx, "namespace", domainProofNS, "", "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if err := create(store.AppendInput{EntryType: "namespace", Namespace: domainProofNS,
			PayloadRaw: []byte(`{"description":"namespace-to-domain bindings this node has verified"}`)}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := s.Store.Resolve(ctx, "registry", domainProofNS, domainProofRegistry, "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if err := create(store.AppendInput{EntryType: "registry", Namespace: domainProofNS, Registry: domainProofRegistry,
			PayloadRaw: []byte(`{"description":"DNS TXT proofs of control over a namespace's declared domain"}`)}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}
