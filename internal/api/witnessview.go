package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// The witness read plane.
//
// Verdicts have always been in the log, under the reserved `_witness`
// namespace, and they were readable — but only by a caller who already knew
// three things: that the namespace is called `_witness`, that the registry name
// is the target's log origin (slash and all, so percent-encoded in a path), and
// that `_`-prefixed namespaces are hidden from the spec read endpoints unless
// you append `?internal=1`. Miss any one and the answer is a 404 that reads
// exactly like "this node witnesses nobody".
//
// That is the wrong shape for this particular claim. A verdict is not directory
// data that happens to live in a reserved namespace; it is the one thing a
// stranger is *supposed* to come and read, because it is how they check that a
// node they do not trust has not rewritten its history. Reaching it through a
// parameter named `internal` says the opposite of what is true.
//
// So verdicts get their own surface, and the hiding rule stays exactly as it
// is: `_witness` remains absent from lookup/query/versions so a crawler does
// not index this node's bookkeeping as directory data, and `?internal=1`
// remains for the explorer UI. Nothing is un-hidden — the claim is simply
// published where it belongs, under a name that says what it is. See issue #27.

// witnessTargetView renders what this node has verified about one target.
//
// Verdict and health are reported side by side and must stay distinguishable.
// The verdict is *evidence*: a size and a root backed by a consistency proof
// that a reader checks against the target's own key, with this node nowhere in
// the path. The health is this node's report about itself and is evidence of
// nothing at all.
//
// Both are needed, because either alone misleads. A verdict is only rewritten
// when the target's tree changes, so a witness loop that has been failing for
// hours still shows a last verdict reading consistency_ok — indistinguishable
// from a check that ran a second ago and found nothing new. And health alone
// says the loop is running without saying what it concluded.
func (s *Server) witnessTargetView(ctx context.Context, origin string, reg store.Entry) map[string]any {
	out := map[string]any{
		"origin": origin,
		// Where to fetch the target's own checkpoint, so the reader can repeat
		// this verification instead of accepting our summary of it.
		"verdict_url": "/dedi/witness/" + url.PathEscape(origin),
	}
	// internal/witness writes the target URL as a plain top-level field on the
	// registry payload, beside the description.
	var rp struct {
		Target string `json:"target"`
	}
	if json.Unmarshal(reg.PayloadRaw, &rp) == nil && rp.Target != "" {
		out["target_url"] = rp.Target
	}
	// The target's verifier key, where this node knows it. Public by
	// construction — a verifier key is exactly what you hand out so others can
	// check you — and without it a reader cannot validate the target's
	// checkpoint signature themselves and is left trusting us.
	if origin == s.WitnessTarget && s.WitnessTargetKey != "" {
		out["target_key"] = s.WitnessTargetKey
	}

	e, err := s.Store.Resolve(ctx, "record", witnessNamespace, origin, "checkpoint", nil, nil)
	if err != nil {
		// A registry with no verdict: enrolled, never successfully checked. Said
		// plainly, because omitting the fields would let a caller reading with
		// `.consistency_ok // true` treat "never verified" as "verified fine".
		out["witnessed"] = false
		return out
	}
	var v struct {
		Size          int64  `json:"size"`
		Root          string `json:"root"`
		ConsistencyOK bool   `json:"consistency_ok"`
		Detail        string `json:"detail"`
	}
	if json.Unmarshal(e.PayloadRaw, &v) != nil {
		out["witnessed"] = false
		return out
	}
	out["witnessed"] = true
	out["size"] = v.Size
	out["root"] = v.Root
	out["consistency_ok"] = v.ConsistencyOK
	out["state"] = e.State
	out["verdict_at"] = fmtTime(e.CreatedAt)
	out["version_num"] = e.VersionNum
	if v.Detail != "" {
		// Only set when the witness caught something — today, a root that
		// changed under an unchanged tree size. It is the whole reason anyone
		// reads this endpoint, so it is never summarised away.
		out["detail"] = v.Detail
	}
	if h := s.witnessHealthFor(origin); h != nil {
		out["health"] = h
	}
	return out
}

// witnessHealthFor finds the liveness of whichever loop watches this origin.
//
// There are two kinds and they are reported through different hooks: one ring
// peer that this node witnesses by configuration, and one loop per delegated
// child. A single field would make a stalled child-witness invisible behind a
// healthy ring one.
func (s *Server) witnessHealthFor(origin string) map[string]any {
	if origin == s.WitnessTarget && s.WitnessHealth != nil {
		return witnessHealth(s.WitnessHealth())
	}
	if s.ChildWitnessHealth != nil {
		if state, running := s.ChildWitnessHealth(origin); running {
			return witnessHealth(state)
		}
		return map[string]any{"checking": false, "stale": true,
			"last_error": "no witness loop is running for this target"}
	}
	return nil
}

// witnessTargets lists every target this node holds a witness registry for.
func (s *Server) witnessTargets(ctx context.Context) ([]store.SummaryRow, error) {
	if _, err := s.Store.Resolve(ctx, "namespace", witnessNamespace, "", "", nil, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Witnessing nobody is a legitimate configuration, not an error, and
			// it answers 200 with an empty list. A 404 here would be read as
			// "this node is too old to have the endpoint", which is a different
			// fact with a different remedy.
			return nil, nil
		}
		return nil, err
	}
	// Page size is deliberately generous: the number of targets is bounded by
	// how many nodes an operator has chosen to witness, not by user input.
	rows, _, err := s.Store.QueryRegistries(ctx, witnessNamespace, store.QueryFilters{PageSize: 100})
	return rows, err
}

// witnessView answers "what has this node verified, and about whom".
func (s *Server) witnessView(w http.ResponseWriter, r *http.Request) {
	rows, err := s.witnessTargets(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	targets := make([]map[string]any, 0, len(rows))
	sound := 0
	for _, row := range rows {
		reg, err := s.Store.Resolve(r.Context(), "registry", witnessNamespace, row.Name, "", nil, nil)
		if err != nil {
			continue
		}
		view := s.witnessTargetView(r.Context(), row.Name, reg)
		if ok, _ := view["consistency_ok"].(bool); ok {
			sound++
		}
		targets = append(targets, view)
	}
	ok(w, "Witness verdicts retrieved successfully", map[string]any{
		"witness": s.origin(),
		"targets": targets,
		"total":   len(targets),
		// Counted here so a monitor does not have to reimplement the reduction
		// and get it subtly wrong. A target with no verdict counts as unsound,
		// because "not yet verified" is not "verified".
		"consistent": sound,
	})
}

// witnessOneView answers for a single target, with the inclusion proof of the
// verdict itself.
//
// The proof is the point of the singular form existing at all. The list says
// what this node concluded; the proof says this node committed to that
// conclusion in its own append-only log at a given position, so it cannot
// later present a different verdict to a different reader without the two
// checkpoints disagreeing. A verdict you cannot pin to a log entry is just an
// assertion over HTTP.
func (s *Server) witnessOneView(w http.ResponseWriter, r *http.Request) {
	origin := r.PathValue("target")
	reg, err := s.Store.Resolve(r.Context(), "registry", witnessNamespace, origin, "", nil, nil)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "witness target")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	view := s.witnessTargetView(r.Context(), origin, reg)

	e, err := s.Store.Resolve(r.Context(), "record", witnessNamespace, origin, "checkpoint", nil, nil)
	if err != nil {
		// Registry but no verdict — witnessTargetView has already said
		// witnessed:false, and there is no entry to prove.
		ok(w, "Witness verdict retrieved successfully", view)
		return
	}
	proof, err := s.buildProof(r, e)
	if err != nil {
		// A node with no checkpointer cannot mint the proof. The verdict is
		// still worth returning — degraded, and visibly so, rather than a 500
		// that hides a readable answer behind an unrelated missing subsystem.
		view["proof_error"] = "no checkpoint covers this verdict"
		ok(w, "Witness verdict retrieved successfully", view)
		return
	}
	okProof(w, "Witness verdict retrieved successfully", view, proof)
}
