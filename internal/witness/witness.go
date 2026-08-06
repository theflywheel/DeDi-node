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
}

// Result is the outcome of a single verification.
type Result struct {
	Size          int64
	Root          string // base64
	ConsistencyOK bool
	Fresh         bool // the target's checkpoint advanced since last time
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
	if _, err := w.Store.Resolve(ctx, "registry", witnessNS, w.Origin, "", nil, nil); errors.Is(err, store.ErrNotFound) {
		p, _ := json.Marshal(map[string]any{"description": "witnessed checkpoints of " + w.Origin, "target": w.TargetURL})
		if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "registry", Namespace: witnessNS, Registry: w.Origin,
			PayloadRaw: p, CreatedBy: "witness"}); err != nil {
			return err
		}
	}
	return nil
}

// VerifyOnce checks the target's latest checkpoint; if it advanced, verifies
// append-only consistency against the last witnessed state and records the
// verdict in this node's own log. A detected inconsistency is recorded with
// state `revoked` so it surfaces as a broken witness.
func (w *Witness) VerifyOnce(ctx context.Context) (Result, error) {
	size, root, err := w.verifyCheckpoint(ctx)
	if err != nil {
		return Result{}, err
	}
	rootB64 := base64.StdEncoding.EncodeToString(root[:])
	last, lastRoot, have := w.lastWitnessed(ctx)

	if have && size == last {
		return Result{Size: size, Root: rootB64, ConsistencyOK: true, Fresh: false}, nil
	}

	consistencyOK := true
	// last == 0 is "the target's log was empty when we last looked", and the
	// empty tree is a prefix of every tree, so there is nothing to prove. It has
	// to be special-cased because ProveTree rejects an old size below 1: without
	// this, a witness that first saw its target empty could never advance again.
	// It would fail on every run, silently, while its stored verdict still read
	// consistency_ok — a witness that has stopped witnessing but still looks fine.
	if have && last > 0 {
		if size < last {
			consistencyOK = false // target shrank — impossible for an append-only log
		} else {
			proof, err := w.fetchConsistency(ctx, last, size)
			if err != nil {
				return Result{}, err
			}
			if err := tlog.CheckTree(proof, size, root, last, lastRoot); err != nil {
				consistencyOK = false
			}
		}
	}

	if err := w.ensureParents(ctx); err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"target": w.TargetURL, "size": size, "root": rootB64, "consistency_ok": consistencyOK,
	})
	state := "live"
	if !consistencyOK {
		state = "revoked"
	}
	if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "record", Namespace: witnessNS, Registry: w.Origin,
		RecordName: "checkpoint", PayloadRaw: payload, State: state, CreatedBy: "witness"}); err != nil {
		return Result{}, err
	}
	return Result{Size: size, Root: rootB64, ConsistencyOK: consistencyOK, Fresh: true}, nil
}

// Health describes whether this witness is still doing its job.
//
// It exists because a stalled witness is otherwise invisible. The verdict in
// the log is only rewritten when the target's tree changes, so a witness that
// has been failing on every run for hours still presents a last verdict reading
// consistency_ok — indistinguishable from one that checked a second ago and
// found nothing new. Verdict age cannot stand in for this: on a quiet target the
// newest verdict is legitimately old.
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
	now := time.Now().UTC()
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

// Run verifies on Interval until ctx is done.
func (w *Witness) Run(ctx context.Context) {
	verify := func() {
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
	verify()
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			verify()
		}
	}
}
