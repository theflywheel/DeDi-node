// Package witness lets one dedid node independently verify that another node's
// log is append-only, using the target's signed checkpoints and consistency
// proofs. This is the decentralised-trust primitive: a relying party no longer
// has to trust the target's operator alone — an independent witness will detect
// any attempt to rewrite history. Each verification is itself recorded in the
// witness's own append-only log, under the reserved `_witness` namespace.
package witness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
	"github.com/theflywheel/DeDi-node/internal/store"
)

const witnessNS = "_witness"

// Appender is how a verdict reaches the log. On a standalone node it is the
// store itself; in a cluster it is the Raft proposer, so a verdict is
// replicated like any other entry rather than written to one replica's
// database where the others would never see it.
type Appender interface {
	Append(ctx context.Context, in store.AppendInput) (store.Entry, error)
}

// Witness periodically verifies a target node and records the verdict.
type Witness struct {
	Store     *store.Store
	TargetURL string // target base URL including /dedi, e.g. https://a.example/dedi
	TargetKey string // target node verifier key (sumdb/note format)
	Origin    string // stable label for the target; used as the registry name
	Interval  time.Duration
	Client    *http.Client

	// RecordInterval is the least time between two consistent verdicts this
	// witness appends for the target. Every check still runs every Interval;
	// only the writing of an "ok" verdict waits. Alarms are never held back.
	//
	// It exists because a verdict is itself a log entry. In a ring (A watches
	// B, B watches C, C watches A) each verdict grows the witness's own tree,
	// which its own watcher then sees as a change and records, and so on round
	// the ring for ever: about one verdict per node per check, with nothing
	// else happening. Zero records every change, which is the old behaviour.
	RecordInterval time.Duration

	// now is the clock RecordInterval is measured against; nil is time.Now.
	// Tests swap it rather than sleeping for an hour.
	now func() time.Time

	// Writer records verdicts. nil writes straight to Store.
	Writer Appender

	// IsWriter reports whether this process should be running the witness loop
	// at all. In a cluster only the leader appends, so followers stand by; nil
	// means "always", which is what a standalone node wants.
	//
	// Followers deliberately do not verify. Three replicas independently
	// polling the same target would triple the load on it to learn the same
	// fact, and the verdict is replicated to them anyway.
	IsWriter func() bool

	mu     sync.Mutex
	health Health

	// The log is the authority: every check proves consistency from the newest
	// logged verdict, as before RecordInterval existed. seen adds to that. It
	// is the last tree this witness verified but did not log, and seenOver is
	// the logged verdict it was proven from. With RecordInterval the log can
	// lag what was checked, and a rewrite of entries between the logged tree
	// and seen, undone or not, passes a proof from the logged tree alone; so
	// each check also proves from seen.
	//
	// seen is used only while seenOver is still the newest logged verdict.
	// Once anything else has been logged (this witness's own alarm, another
	// replica that led meanwhile, or a second loop given the same origin) seen
	// is dropped, because nothing proved it consistent with the new verdict
	// and an "ok" for it might be false.
	//
	// All of this lives only in memory, and recordedAt is when this witness
	// last appended an "ok" for the tree it had just checked (alarms, and the
	// "ok" written ahead of one, do not move it). After a restart, a crash or a leadership change, seen is
	// gone: up to RecordInterval of verified but unlogged history is lost, and
	// a rewrite confined to that span is not alarmed afterwards. Nothing
	// flushes seen on shutdown, because a step-down leaves this node unable to
	// write and the main process does not wait for witness loops to stop.
	// recordedAt also starts at zero, so the first change is logged at once.
	seen       *tree
	seenOver   tree
	seenOverOK bool // false: the log held no verdict when seen was verified
	recordedAt time.Time
}

type tree struct {
	size int64
	root tlog.Hash
}

// Result is the outcome of a single verification.
type Result struct {
	Size          int64
	Root          string // base64
	ConsistencyOK bool
	Fresh         bool // this run appended a verdict
}

func (w *Witness) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// unlogged returns seen if it was proven from the logged verdict given, which
// must still be the newest one, and is not that verdict itself.
func (w *Witness) unlogged(logT tree, logged bool) (tree, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil || w.seenOverOK != logged || w.seenOver != logT || (logged && *w.seen == logT) {
		return tree{}, false
	}
	return *w.seen, true
}

// remember stores the tree just verified, the logged verdict it was proven
// from (or, if it was just appended, itself), and when a verdict was appended.
func (w *Witness) remember(t, over tree, overOK, appended bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen, w.seenOver, w.seenOverOK = &t, over, overOK
	if appended {
		w.recordedAt = w.clock()
	}
}

// recordDue reports whether a consistent verdict may be appended now.
func (w *Witness) recordDue() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.RecordInterval <= 0 || w.recordedAt.IsZero() || w.clock().Sub(w.recordedAt) >= w.RecordInterval
}

func (w *Witness) appender() Appender {
	if w.Writer != nil {
		return w.Writer
	}
	return w.Store
}

func (w *Witness) httpClient() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (w *Witness) get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.TargetURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// verifyCheckpoint fetches the target checkpoint and verifies its signature.
func (w *Witness) verifyCheckpoint(ctx context.Context) (int64, tlog.Hash, error) {
	body, code, err := w.get(ctx, "/log/checkpoint")
	if err != nil {
		return 0, tlog.Hash{}, err
	}
	if code != http.StatusOK {
		return 0, tlog.Hash{}, fmt.Errorf("checkpoint status %d", code)
	}
	verifier, err := note.NewVerifier(w.TargetKey)
	if err != nil {
		return 0, tlog.Hash{}, fmt.Errorf("target verifier key: %w", err)
	}
	n, err := note.Open(body, note.VerifierList(verifier))
	if err != nil {
		return 0, tlog.Hash{}, fmt.Errorf("checkpoint signature: %w", err)
	}
	_, size, root, err := merkle.ParseCheckpoint(n.Text)
	return size, root, err
}

// lastWitnessed returns the size and root this witness last recorded for the target.
func (w *Witness) lastWitnessed(ctx context.Context) (int64, tlog.Hash, bool) {
	e, err := w.Store.Resolve(ctx, "record", witnessNS, w.Origin, "checkpoint", nil, nil)
	if err != nil {
		return 0, tlog.Hash{}, false
	}
	var rec struct {
		Size int64  `json:"size"`
		Root string `json:"root"`
	}
	if json.Unmarshal(e.PayloadRaw, &rec) != nil {
		return 0, tlog.Hash{}, false
	}
	rb, err := base64.StdEncoding.DecodeString(rec.Root)
	if err != nil || len(rb) != len(tlog.Hash{}) {
		return 0, tlog.Hash{}, false
	}
	var h tlog.Hash
	copy(h[:], rb)
	return rec.Size, h, true
}

func (w *Witness) fetchConsistency(ctx context.Context, old, size int64) (tlog.TreeProof, error) {
	body, code, err := w.get(ctx, fmt.Sprintf("/log/proof/consistency?old=%d&new=%d", old, size))
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("consistency status %d", code)
	}
	var env struct {
		Data struct {
			Proof []string `json:"proof"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	proof := make(tlog.TreeProof, len(env.Data.Proof))
	for i, s := range env.Data.Proof {
		hb, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(hb) != len(tlog.Hash{}) {
			return nil, fmt.Errorf("bad consistency proof hash")
		}
		copy(proof[i][:], hb)
	}
	return proof, nil
}

// ensureParents creates the _witness namespace and target registry on first use.
func (w *Witness) ensureParents(ctx context.Context) error {
	if _, err := w.Store.Resolve(ctx, "namespace", witnessNS, "", "", nil, nil); errors.Is(err, store.ErrNotFound) {
		if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: witnessNS,
			PayloadRaw: []byte(`{"description":"checkpoints this node has independently witnessed"}`), CreatedBy: "witness"}); err != nil {
			return err
		}
	}
	reg, err := w.Store.Resolve(ctx, "registry", witnessNS, w.Origin, "", nil, nil)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "registry", Namespace: witnessNS, Registry: w.Origin,
			PayloadRaw: w.registryPayload(), CreatedBy: "witness"}); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Upgrade path. Registries created before the target key was recorded
		// carry no `target_key`, and the verdicts filed under them are then
		// unre-checkable by anyone who does not already hold the target's key —
		// which is every reader the endpoint exists for. Appending a new
		// registry version is how this log corrects anything.
		var have struct {
			TargetKey string `json:"target_key"`
		}
		if json.Unmarshal(reg.PayloadRaw, &have) == nil && have.TargetKey != w.TargetKey && w.TargetKey != "" {
			if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "registry", Namespace: witnessNS,
				Registry: w.Origin, PayloadRaw: w.registryPayload(), CreatedBy: "witness"}); err != nil {
				return err
			}
		}
	}
	return nil
}

// registryPayload describes the target a verdict is about.
//
// The verifier key is recorded here rather than left to the read plane to find,
// because the read plane cannot find it for a delegated child: the child's key
// lives in a delegation record in some parent namespace, and the witness
// registry does not know which. Every witness — ring peer or child — already
// holds the key it verifies against, so the honest place for it is the registry
// that says what is being verified.
//
// It is a *verifier* key, published precisely so others can check the target
// without asking us to vouch for it.
func (w *Witness) registryPayload() []byte {
	p := map[string]any{
		"description": "witnessed checkpoints of " + w.Origin,
		"target":      w.TargetURL,
	}
	if w.TargetKey != "" {
		p["target_key"] = w.TargetKey
	}
	b, _ := json.Marshal(p)
	return b
}

// diverges reports whether cur cannot be an append-only extension of base,
// with a detail for the one case a verdict explains in words. An error is a
// failure to check, not a finding.
func (w *Witness) diverges(ctx context.Context, base, cur tree) (bool, string, error) {
	switch {
	case cur.size == base.size:
		// An unchanged tree is only unchanged if its root still agrees.
		// Returning early on size alone accepted the one attack that needs no
		// growth at all: swap a leaf, re-sign at the same height, and a witness
		// comparing sizes sees nothing to check. "Append-only" is a claim about
		// content, and the root is the only thing that measures content — so a
		// matching size with a different root is not a quiet period, it is
		// equivocation, and it is recorded as an alarm rather than skipped.
		if cur.root != base.root {
			return true, "root changed while tree size stayed at " + fmt.Sprint(cur.size) +
				": history was rewritten in place", nil
		}
		return false, "", nil
	case base.size == 0:
		// "The target's log was empty when we last looked", and the empty tree
		// is a prefix of every tree, so there is nothing to prove. It has to be
		// special-cased because ProveTree rejects an old size below 1: without
		// this, a witness that first saw its target empty could never advance
		// again. It would fail on every run, silently, while its stored verdict
		// still read consistency_ok — a witness that has stopped witnessing but
		// still looks fine.
		return false, "", nil
	case cur.size < base.size:
		return true, "", nil // target shrank — impossible for an append-only log
	}
	proof, err := w.fetchConsistency(ctx, base.size, cur.size)
	if err != nil {
		return false, "", err
	}
	if tlog.CheckTree(proof, cur.size, cur.root, base.size, base.root) != nil {
		return true, "", nil
	}
	return false, "", nil
}

// appendVerdict writes one verdict about t: live when consistent, revoked
// when not.
func (w *Witness) appendVerdict(ctx context.Context, t tree, ok bool, detail string) error {
	p := map[string]any{"target": w.TargetURL, "size": t.size,
		"root": base64.StdEncoding.EncodeToString(t.root[:]), "consistency_ok": ok}
	if detail != "" {
		p["detail"] = detail
	}
	state := "live"
	if !ok {
		state = "revoked"
	}
	payload, _ := json.Marshal(p)
	_, err := w.appender().Append(ctx, store.AppendInput{EntryType: "record", Namespace: witnessNS,
		Registry: w.Origin, RecordName: "checkpoint", PayloadRaw: payload, State: state, CreatedBy: "witness"})
	return err
}

// VerifyOnce checks the target's latest checkpoint and verifies append-only
// consistency from the newest logged verdict and, when this witness has
// verified a newer tree than that without logging it, from that tree too. An
// inconsistency with either is appended to this node's own log at once, with
// state `revoked` so it surfaces as a broken witness. A consistent verdict is
// appended only if RecordInterval has passed since this witness last appended
// one; a check in between still ran, and was proven from the logged verdict,
// so the next appended "ok" is consistent with the one before it and the log
// alone remains a checkable chain.
func (w *Witness) VerifyOnce(ctx context.Context) (Result, error) {
	size, root, err := w.verifyCheckpoint(ctx)
	if err != nil {
		return Result{}, err
	}
	cur := tree{size, root}
	res := Result{Size: size, Root: base64.StdEncoding.EncodeToString(root[:]), ConsistencyOK: true}

	// Before the early return below, not after it. The parents describe the
	// target — including the verifier key a reader needs to re-check any verdict
	// filed under them — and that description has to be able to catch up even
	// when the verdict itself does not change. Left where it used to be, an
	// existing registry missing its key would stay that way for as long as the
	// target's tree was quiet, which on a working ring is most of the time.
	if err := w.ensureParents(ctx); err != nil {
		return Result{}, err
	}
	logSize, logRoot, logged := w.lastWitnessed(ctx)
	logT := tree{logSize, logRoot}

	alarm := func(detail string) (Result, error) {
		if err := w.appendVerdict(ctx, cur, false, detail); err != nil {
			return Result{}, err
		}
		// The alarmed tree is now the newest logged verdict, and so the
		// baseline the next check proves from: a target that keeps serving it
		// raises one alarm, not one per check. Anything remembered in memory
		// was proven from an older verdict and is ignored from here on.
		res.ConsistencyOK, res.Fresh = false, true
		return res, nil
	}

	if logged {
		bad, detail, err := w.diverges(ctx, logT, cur)
		if err != nil {
			return Result{}, err
		}
		if bad {
			return alarm(detail)
		}
	}
	if seen, ok := w.unlogged(logT, logged); ok {
		bad, detail, err := w.diverges(ctx, seen, cur)
		if err != nil {
			return Result{}, err
		}
		if bad {
			// The tree this contradicts is not in the log, so an alarm alone
			// could not be re-checked by anyone reading it. seen was proven
			// consistent with the newest logged verdict, which is still the
			// newest (unlogged checks that), so logging it as consistent is
			// true, and puts the contradicting pair in the log.
			if err := w.appendVerdict(ctx, seen, true, ""); err != nil {
				return Result{}, err
			}
			return alarm(detail)
		}
	}

	// Consistent. Nothing to write if the log already says exactly this, and
	// nothing yet if this witness logged a verdict less than RecordInterval ago.
	if (logged && cur == logT) || !w.recordDue() {
		w.remember(cur, logT, logged, false)
		return res, nil
	}
	if err := w.appendVerdict(ctx, cur, true, ""); err != nil {
		return Result{}, err
	}
	w.remember(cur, cur, true, true)
	res.Fresh = true
	return res, nil
}

// Health describes whether this witness is still doing its job.
//
// It exists because a stalled witness is otherwise invisible. The verdict in
// the log is only rewritten when the target's tree changes (and, while it stays
// consistent, at most once per RecordInterval), so a witness that has been
// failing on every run for hours still presents a last verdict reading
// consistency_ok — indistinguishable from one that checked a second ago and
// found nothing new. Verdict age cannot stand in for this: on a quiet target, or
// inside RecordInterval, the newest verdict is legitimately old.
//
// What is trustworthy here is only that this node believes it ran. It is this
// node's report about itself and nobody should take it as proof; the proof is
// the verdict and its consistency chain. This is an operational signal, for
// noticing that the proof has stopped being refreshed.
type Health struct {
	LastAttemptAt time.Time // when a check was last started
	LastSuccessAt time.Time // when a check last completed without error
	LastError     string    // why the most recent check failed; empty if it did not
	Attempts      int64
	Failures      int64
	Interval      time.Duration

	// Standby means this replica is deliberately not witnessing because it is
	// not the cluster's writer. Without it a follower would look identical to a
	// stalled witness — no recent attempt, no recent success — and the liveness
	// check would raise an alarm on two of every three healthy replicas.
	Standby bool
}

// Status returns a snapshot of this witness's own liveness.
func (w *Witness) Status() Health {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := w.health
	h.Interval = w.Interval
	return h
}

// standing reports whether this process should witness right now.
func (w *Witness) standing() bool {
	if w.IsWriter == nil {
		return true
	}
	return w.IsWriter()
}

func (w *Witness) recordStandby() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.health.Standby = true
	w.health.LastError = ""
}

func (w *Witness) record(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.health.Standby = false
	now := w.clock().UTC()
	w.health.LastAttemptAt = now
	w.health.Attempts++
	if err != nil {
		w.health.Failures++
		w.health.LastError = err.Error()
		return
	}
	w.health.LastSuccessAt = now
	w.health.LastError = ""
}

// check is one tick of Run: verify, then record health. Health is recorded on
// every check, whether or not it appended a verdict, so a witness that is
// checking but has nothing new to write still reads as alive.
func (w *Witness) check(ctx context.Context) {
	if !w.standing() {
		w.recordStandby()
		return
	}
	r, err := w.VerifyOnce(ctx)
	w.record(err)
	switch {
	case err != nil:
		log.Printf("witness(%s): %v", w.Origin, err)
	case r.Fresh && r.ConsistencyOK:
		log.Printf("witness(%s): verified append-only at size %d", w.Origin, r.Size)
	case r.Fresh && !r.ConsistencyOK:
		log.Printf("witness(%s): ALARM — consistency FAILED at size %d", w.Origin, r.Size)
	}
}

// Run verifies on Interval until ctx is done.
func (w *Witness) Run(ctx context.Context) {
	w.check(ctx)
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.check(ctx)
		}
	}
}
