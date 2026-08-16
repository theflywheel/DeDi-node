package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/provision"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// The child-node plane: minting a delegation, redeeming it, and reading the
// result. See internal/delegation for why enrolment is two steps and why the
// parent never generates the child's key.
//
// Three routes, and they are authenticated three different ways on purpose:
//
//   - Minting is a publisher write, signed and scoped to the parent namespace.
//     Granting away a slice of your namespace is at least as consequential as
//     publishing a record in it.
//   - Enrolment is authenticated by the one-time token alone. The child cannot
//     hold a publisher key yet — being handed one is the thing it is asking
//     for — so the token is the only credential it can present.
//   - Reading delegations is public. Who holds authority over a namespace is
//     exactly the question relying parties need answered, and answering it only
//     to authenticated callers would make the delegation unverifiable by the
//     people it exists to inform.

type createChildRequest struct {
	Namespace string `json:"namespace"` // full child namespace, e.g. beckn.mobility
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Image     string `json:"image"`
	DBURL     string `json:"db_url"`
	PublicURL string `json:"public_url"`
}

// defaultImage is used when the caller does not name one. It is a placeholder
// rather than a guess at the operator's registry: a wrong-but-plausible image
// reference fails at pull time with a message about the image, long after the
// operator has stopped thinking about this form.
const defaultImage = "ghcr.io/theflywheel/dedi-node:latest"

// createChild mints a delegation offer for a child namespace and returns the
// rendered deploy artifact for it.
//
// The offer is appended to the log *before* the artifact is returned. If the
// append fails the operator gets an error and no token, rather than a token
// that no record backs — which would enrol successfully against nothing and
// leave a child claiming a namespace the parent never granted.
func (s *Server) createChild(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	parentNS := r.PathValue("namespace")

	var req createChildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "body must be JSON of the form {\"namespace\": \"parent.child\", ...}")
		return
	}
	provider, pOK := provision.Get(orDefault(req.Provider, "env"))
	if !pOK {
		badRequest(w, fmt.Sprintf("unknown provider %q; available: %s",
			req.Provider, strings.Join(provision.Names(), ", ")))
		return
	}

	now := time.Now().UTC()
	offer, err := delegation.NewOffer(parentNS, req.Namespace, req.Label, now)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	// Refuse to overwrite a live delegation. Re-minting over an active child
	// would hand its namespace to whoever redeems the new token, which is an
	// account takeover of the child dressed up as a convenience.
	switch cur, err := s.currentDelegation(r.Context(), parentNS, offer.Namespace); {
	case err != nil && !errors.Is(err, store.ErrNotFound):
		internal(w, err)
		return
	case err == nil && cur.State == delegation.StateActive:
		stateConflict(w, fmt.Errorf("%s is already delegated to %s; revoke it before re-issuing",
			offer.Namespace, cur.ChildOrigin))
		return
	}

	if err := s.ensureDelegationRegistry(r.Context(), parentNS); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	payload, _ := json.Marshal(offer.Payload())
	in := store.AppendInput{
		EntryType: "record", Namespace: parentNS, Registry: delegation.Registry,
		RecordName: offer.Namespace, PayloadRaw: payload,
	}
	if _, err := s.writerAppend(r, key.KID, in); err != nil {
		s.writeFailure(w, r, err)
		return
	}

	art, err := provider.Render(provision.Spec{
		NodeName:    nodeNameFor(offer.Namespace),
		Origin:      offer.Namespace + "/log",
		Namespace:   offer.Namespace,
		Image:       orDefault(req.Image, defaultImage),
		ParentURL:   s.publicBase(r),
		ParentKey:   s.VerifierKey,
		EnrolToken:  offer.Token,
		PublicURL:   strings.TrimRight(req.PublicURL, "/"),
		DatabaseURL: req.DBURL,
	})
	if err != nil {
		internal(w, err)
		return
	}

	// The token is returned exactly once, here. It is not stored in plaintext
	// anywhere — the log holds only its hash — so an operator who loses this
	// response mints a new offer rather than recovering this one.
	writeJSON(w, http.StatusOK, envelope{
		Message: "Delegation offer created — deploy the child within the hour",
		Data: map[string]any{
			"namespace":  offer.Namespace,
			"token":      offer.Token,
			"expires_at": offer.ExpiresAt.Format(time.RFC3339),
			"artifact":   art,
			"providers":  provision.Names(),
		},
	})
}

// enrolChild redeems an offer. Authenticated by the token alone; see the plane
// note above for why it cannot be a signed write.
func (s *Server) enrolChild(w http.ResponseWriter, r *http.Request) {
	var en delegation.Enrolment
	if err := json.NewDecoder(r.Body).Decode(&en); err != nil {
		badRequest(w, "body must be JSON of the form {\"namespace\":…, \"token\":…, \"origin\":…, \"key\":…, \"url\":…}")
		return
	}
	parentNS, known := s.parentNamespaceOf(en.Namespace)
	if !known {
		// Deliberately the same shape of answer as a bad token: an unenrolled
		// caller should not be able to map which namespaces this node has
		// offers outstanding for by watching which ones answer differently.
		unauthorized(w, "enrolment rejected")
		return
	}
	cur, err := s.currentDelegation(r.Context(), parentNS, en.Namespace)
	if errors.Is(err, store.ErrNotFound) {
		unauthorized(w, "enrolment rejected")
		return
	} else if err != nil {
		internal(w, err)
		return
	}

	next, err := delegation.Redeem(cur, en, time.Now().UTC())
	switch {
	case errors.Is(err, delegation.ErrBadToken), errors.Is(err, delegation.ErrNotDelegate):
		unauthorized(w, "enrolment rejected")
		return
	case errors.Is(err, delegation.ErrTokenUsed):
		stateConflict(w, err)
		return
	case errors.Is(err, delegation.ErrTokenStale):
		writeErr(w, http.StatusGone, "TOKEN_EXPIRED", err.Error())
		return
	case errors.Is(err, delegation.ErrBadRequest):
		badRequest(w, err.Error())
		return
	case err != nil:
		internal(w, err)
		return
	}

	payload, _ := json.Marshal(next)
	in := store.AppendInput{
		EntryType: "record", Namespace: parentNS, Registry: delegation.Registry,
		RecordName: en.Namespace, PayloadRaw: payload,
		// The redeeming write must land on the version we just read. Without
		// this, two children racing the same leaked token could both read the
		// offer and both enrol, and the second would silently replace the
		// first as the holder of the namespace.
		ExpectedPrevDigest: mustDigest(r.Context(), s, parentNS, en.Namespace),
		ExpectedPrevState:  "live",
	}
	if _, err := s.writerAppend(r, "enrolment:"+en.Namespace, in); err != nil {
		s.writeFailure(w, r, err)
		return
	}

	// Start watching the child now rather than at the next restart. A
	// delegation that only becomes visible after a redeploy makes the admin
	// panel look broken at precisely the moment the operator is checking it.
	if s.Network != nil {
		s.Network.Add(en.Origin, strings.TrimRight(en.URL, "/"))
	}
	if s.OnDelegation != nil {
		s.OnDelegation(next)
	}

	ok(w, "Enrolled successfully", map[string]any{
		"namespace": next.Namespace, "state": next.State,
		"parent_origin": s.origin(), "parent_key": s.VerifierKey,
	})
}

// deprecatedEnrolChild is the alias for POST /dedi/enrol, kept only until
// deployed ring nodes upgrade to call POST /enrol directly. It answers
// exactly like enrolChild but marks itself deprecated so callers can detect
// and migrate off it.
func (s *Server) deprecatedEnrolChild(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Deprecation", "true")
	s.enrolChild(w, r)
}

type revokeChildRequest struct {
	Reason string `json:"reason"`
}

// revokeChild withdraws a delegation.
//
// Signed and scoped exactly like minting, because it is the same authority
// exercised in the other direction: whoever can grant a slice of a namespace is
// whoever can take it back, and nobody else.
//
// This is the transition that was missing when the rest of delegation shipped.
// Without it an operator who delegated the wrong namespace, or to the wrong
// party, had no supported move at all — re-minting is refused over a live
// delegation precisely because it would hand the namespace to whoever redeemed
// the new token, so the absence of revocation made the refusal a dead end
// rather than a safety rail.
func (s *Server) revokeChild(w http.ResponseWriter, r *http.Request) {
	if _, ok := scoped(w, r); !ok {
		return
	}
	parentNS, childNS := r.PathValue("namespace"), r.PathValue("child")

	var req revokeChildRequest
	// A body is optional here: revoking without stating a reason is worse
	// record-keeping, not an invalid request, and refusing the call over it
	// would push operators toward leaving a bad delegation in place.
	_ = json.NewDecoder(r.Body).Decode(&req)

	cur, err := s.currentDelegation(r.Context(), parentNS, childNS)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, fmt.Sprintf("%s is not delegated by %s", childNS, parentNS))
		return
	} else if err != nil {
		internal(w, err)
		return
	}

	next, err := delegation.Revoke(cur, req.Reason, time.Now().UTC())
	if errors.Is(err, delegation.ErrRevoked) {
		stateConflict(w, err)
		return
	} else if err != nil {
		internal(w, err)
		return
	}

	payload, _ := json.Marshal(next)
	if _, err := s.writerAppend(r, "revocation:"+childNS, store.AppendInput{
		EntryType: "record", Namespace: parentNS, Registry: delegation.Registry,
		RecordName: childNS, PayloadRaw: payload,
		// Same precondition as enrolment, for the mirror-image race: a
		// revocation racing an enrolment must not silently overwrite the
		// enrolment and leave a record that says "revoked" about a child the
		// parent is meanwhile happily witnessing.
		ExpectedPrevDigest: mustDigest(r.Context(), s, parentNS, childNS),
		ExpectedPrevState:  "live",
	}); err != nil {
		s.writeFailure(w, r, err)
		return
	}

	// Stop witnessing before answering. A witness loop still running against a
	// revoked child would keep appending verdicts about a node this log has
	// just said it no longer vouches for, which reads to anyone downstream as
	// the parent standing behind it after all.
	if s.OnDelegation != nil {
		s.OnDelegation(next)
	}
	if s.Network != nil && cur.ChildURL != "" {
		s.Network.Remove(cur.ChildURL)
	}

	ok(w, "Delegation revoked", map[string]any{
		"namespace": next.Namespace, "state": next.State, "revoked_at": next.RevokedAt,
		"note": "The child node keeps running and keeps its own log. What changed is that " +
			"this node's log now records, verifiably, that the authority is withdrawn.",
	})
}

// listDelegations answers who holds authority under this node's namespaces.
func (s *Server) listDelegations(w http.ResponseWriter, r *http.Request) {
	rows, _, err := s.Store.QueryRecords(r.Context(), r.PathValue("namespace"), delegation.Registry, store.QueryFilters{})
	if errors.Is(err, store.ErrNotFound) {
		ok(w, "Delegations retrieved successfully", map[string]any{"children": []any{}, "total": 0})
		return
	} else if err != nil {
		internal(w, err)
		return
	}
	// QueryRecords returns summaries without payloads, so each delegation is
	// resolved for its body. The list is one row per child and changes only
	// when a child is added or revoked, so the extra reads are bounded by the
	// number of children rather than by traffic.
	ns := r.PathValue("namespace")
	children := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		rec, err := s.currentDelegation(r.Context(), ns, row.Name)
		if err != nil {
			continue
		}
		child := map[string]any{
			"namespace": rec.Namespace, "label": rec.Label, "state": rec.State,
			"child_origin": rec.ChildOrigin, "child_url": rec.ChildURL,
			// Published so a browser can verify the child's checkpoints against
			// the child's own key, exactly as it already can for a witness
			// target — the parent stays out of the trust path.
			"child_key": rec.ChildKey, "enrolled_at": rec.EnrolledAt,
			// A revocation is published, not merely recorded. Someone holding a
			// signature from this child needs to be able to see that the grant
			// behind it was withdrawn, and when — which is only useful if the
			// public read surface says so.
			"revoked_at": rec.RevokedAt, "reason": rec.Reason,
		}
		// When an offer expires, but never the hash it would be checked against.
		// Without this, a stale offer and a fresh one are the same row, and the
		// operator cannot tell whether to wait for the child to boot or to mint
		// again — the one question this row exists to answer while it says
		// "offered".
		if rec.State == delegation.StateOffered && rec.ExpiresAt != "" {
			child["expires_at"] = rec.ExpiresAt
			if exp, err := time.Parse(time.RFC3339, rec.ExpiresAt); err == nil {
				child["expired"] = time.Now().UTC().After(exp)
			}
		}
		// Whether the node answers at all, which the console otherwise had to
		// leave to open the explorer page for. Kept as its own field rather
		// than folded into the witness block: reachable is a fact about the
		// network this second, and the verdict is evidence about a log. A node
		// that is up proves nothing, and a node that is briefly down disproves
		// nothing — merging them would let uptime read as verification, which
		// is the one confusion this whole design spends its effort avoiding.
		if rec.State == delegation.StateActive && s.Network != nil && rec.ChildURL != "" {
			for _, st := range s.Network.Snapshot() {
				if st.URL != rec.ChildURL {
					continue
				}
				node := map[string]any{"reachable": st.Reachable}
				if !st.CheckedAt.IsZero() {
					node["checked_at"] = st.CheckedAt.UTC().Format(time.RFC3339)
				}
				if st.Error != "" {
					node["error"] = st.Error
				}
				if st.LatencyMS > 0 {
					node["latency_ms"] = st.LatencyMS
				}
				child["node"] = node
				break
			}
		}
		// The verdict is the point of the delegation being witnessed at all, and
		// leaving it out of this list made a child whose log had stopped being
		// append-only read exactly like a healthy one.
		if rec.State == delegation.StateActive {
			if wit := s.childWitness(r.Context(), rec.ChildOrigin); wit != nil {
				child["witness"] = wit
			}
		}
		children = append(children, child)
	}
	ok(w, "Delegations retrieved successfully", map[string]any{
		"children": children, "total": len(children),
	})
}

// --- helpers ---

// witnessNamespace is where witness verdicts live, mirroring the reserved
// namespace internal/witness writes to. Duplicated as a constant rather than
// imported because witness's own tests import this package, and the import
// would be a cycle.
const witnessNamespace = "_witness"

// childWitness renders what this node has verified about one child.
//
// Two different things, deliberately reported side by side:
//
//   - The *verdict* — size, root, consistency_ok — which is evidence. It is
//     backed by a consistency proof a reader can check against the child's own
//     key, without this node in the path.
//   - The *health* of the loop that produces it, which is not evidence at all;
//     it is this node's report about itself.
//
// They have to appear together because the verdict alone lies by omission. A
// verdict is only rewritten when the child's tree changes, so a witness loop
// that has been failing for hours still shows its last verdict reading
// consistency_ok — indistinguishable from a check that ran a second ago and
// found nothing new. Age cannot substitute either: on a quiet child the newest
// verdict is legitimately old.
func (s *Server) childWitness(ctx context.Context, origin string) map[string]any {
	if origin == "" {
		return nil
	}
	out := map[string]any{}
	e, err := s.Store.Resolve(ctx, "record", witnessNamespace, origin, "checkpoint", nil, nil)
	if err == nil {
		var v struct {
			Size          int64  `json:"size"`
			Root          string `json:"root"`
			ConsistencyOK bool   `json:"consistency_ok"`
		}
		if json.Unmarshal(e.PayloadRaw, &v) == nil {
			out["size"] = v.Size
			out["root"] = v.Root
			out["consistency_ok"] = v.ConsistencyOK
			out["verdict_at"] = e.CreatedAt.UTC().Format(time.RFC3339)
		}
	}
	if s.ChildWitnessHealth != nil {
		if state, running := s.ChildWitnessHealth(origin); running {
			out["health"] = witnessHealth(state)
		} else {
			// No loop for a child this node lists as active. Not a proof
			// failure and not a healthy state either — most often a child
			// enrolled before a restart that Resume did not pick up, which
			// otherwise shows as a verdict quietly frozen at its last value.
			out["health"] = map[string]any{"checking": false, "stale": true,
				"last_error": "no witness loop is running for this child"}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// currentDelegation reads the latest delegation record for a child.
func (s *Server) currentDelegation(ctx context.Context, parentNS, childNS string) (delegation.Record, error) {
	e, err := s.Store.Resolve(ctx, "record", parentNS, delegation.Registry, childNS, nil, nil)
	if err != nil {
		return delegation.Record{}, err
	}
	return delegation.ParseRecord(e.PayloadRaw)
}

func mustDigest(ctx context.Context, s *Server, parentNS, childNS string) []byte {
	e, err := s.Store.Resolve(ctx, "record", parentNS, delegation.Registry, childNS, nil, nil)
	if err != nil {
		return nil
	}
	return e.Digest
}

// parentNamespaceOf finds which of this node's publisher namespaces a child
// sits under. A child namespace is by construction exactly one level below its
// parent, so this is the prefix check that ValidateChildNamespace enforced at
// mint time, run in reverse.
func (s *Server) parentNamespaceOf(childNS string) (string, bool) {
	if s.Auth == nil || s.Auth.Keys == nil {
		return "", false
	}
	for _, ns := range s.Auth.Keys.Namespaces() {
		if delegation.ValidateChildNamespace(ns, childNS) == nil {
			return ns, true
		}
	}
	return "", false
}

// ensureDelegationRegistry creates the reserved registry on first use, the same
// way the witness creates its own.
//
// It creates the parent namespace too if it does not exist yet. A node's first
// action is quite reasonably to delegate — an operator standing up a hierarchy
// has no reason to publish a record under the root first — and without this
// that entirely sensible order failed on the missing parent, as an opaque 500
// naming a namespace the operator thought they already held by virtue of
// holding its key.
func (s *Server) ensureDelegationRegistry(ctx context.Context, parentNS string) error {
	if _, err := s.Store.Resolve(ctx, "namespace", parentNS, "", "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if _, err := s.writer().Append(ctx, store.AppendInput{
			EntryType: "namespace", Namespace: parentNS, CreatedBy: "delegation",
			PayloadRaw: []byte(`{"description":"root namespace"}`),
		}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := s.Store.Resolve(ctx, "registry", parentNS, delegation.Registry, "", nil, nil); !errors.Is(err, store.ErrNotFound) {
		return err
	}
	payload := []byte(`{"description":"child nodes this node has delegated namespaces to"}`)
	_, err := s.writer().Append(ctx, store.AppendInput{
		EntryType: "registry", Namespace: parentNS, Registry: delegation.Registry,
		PayloadRaw: payload, CreatedBy: "delegation",
	})
	return err
}

func (s *Server) writerAppend(r *http.Request, by string, in store.AppendInput) (store.Entry, error) {
	in.CreatedBy = by
	return s.writer().Append(r.Context(), in)
}

// writeFailure maps an append error onto the response, including the
// follower-redirect case every write path needs.
func (s *Server) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cluster.ErrNotLeader):
		s.redirectToLeader(w, r)
	case errors.Is(err, store.ErrInvalidWrite):
		badRequest(w, err.Error())
	case errors.Is(err, store.ErrVersionConflict):
		conflict(w, err)
	default:
		internal(w, err)
	}
}

// publicBase is the URL a child should call back on. It prefers the configured
// public URL: behind a proxy the request's own host is the proxy's, and a child
// told to enrol against an internal address cannot reach it.
func (s *Server) publicBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// nodeNameFor turns beckn.mobility into beckn-mobility: a namespace is dotted,
// but service and volume names on most hosts are not.
func nodeNameFor(ns string) string { return strings.ReplaceAll(ns, ".", "-") }

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
