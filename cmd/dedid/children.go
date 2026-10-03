package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/witness"
)

// childSupervisor runs one witness per delegated child.
//
// A parent witnessing its children is the part of this feature that carries
// actual weight. Delegation on its own is a claim — "this node speaks for that
// namespace" — and a claim in a log is only as good as the log being
// append-only. By verifying each child's checkpoints the parent turns the
// delegation into something a relying party can act on: not just "I granted
// them this" but "and I am continuously checking they have not rewritten what
// they did with it".
//
// It is deliberately *not* mutual. A child witnessing its parent would be
// witnessing the node that granted its authority, which is the least
// independent observer available. Children should be witnessed by the open
// ring like anyone else; this is an additional check, not a substitute for one.
type childSupervisor struct {
	store   *store.Store
	cluster *cluster.Node
	monitor *network.Monitor
	iv      time.Duration
	record  time.Duration // DEDI_WITNESS_RECORD_INTERVAL, same as the peer witness

	mu      sync.Mutex
	running map[string]*childWitness
}

// childWitness is one running loop, kept so its liveness can be reported.
//
// The witness itself is retained rather than only its cancel func because a
// verdict without the health of the loop that wrote it is misleading: verdicts
// are rewritten only when the child's tree changes, so a loop failing on every
// run keeps showing its last consistency_ok indefinitely.
type childWitness struct {
	w      *witness.Witness
	cancel context.CancelFunc
}

func newChildSupervisor(s *store.Store, clu *cluster.Node, mon *network.Monitor, record time.Duration) (*childSupervisor, error) {
	iv, err := time.ParseDuration(envOr("DEDI_CHILD_WITNESS_INTERVAL", "60s"))
	if err != nil {
		return nil, err
	}
	return &childSupervisor{store: s, cluster: clu, monitor: mon, iv: iv, record: record,
		running: map[string]*childWitness{}}, nil
}

// Apply reconciles the witness loops with a delegation record, in whichever
// direction the record has just moved.
//
// One entry point rather than two, because start and stop are the same decision
// read off the same field: a caller that had to remember which one to invoke
// would eventually forget on the revocation path, and the failure there is a
// parent still publishing verdicts about a child it has stopped vouching for.
func (cs *childSupervisor) Apply(ctx context.Context, rec delegation.Record) {
	if rec.State == delegation.StateActive {
		cs.Start(ctx, rec)
		return
	}
	cs.Stop(rec.ChildOrigin)
}

// Stop ends the witness loop for one child. Safe to call for a child that was
// never running, which is the common case on a revoked but never-enrolled
// offer.
func (cs *childSupervisor) Stop(origin string) {
	if cs == nil || origin == "" {
		return
	}
	cs.mu.Lock()
	cw, running := cs.running[origin]
	delete(cs.running, origin)
	cs.mu.Unlock()
	if running {
		cw.cancel()
		log.Printf("delegation: stopped witnessing %s — its delegation is no longer active", origin)
	}
}

// Start begins witnessing one child and adds it to the network view.
//
// Idempotent by child origin: enrolment can be retried, and a child that
// re-enrols must not end up with two witness loops writing the same verdicts
// twice into the log.
func (cs *childSupervisor) Start(ctx context.Context, rec delegation.Record) {
	if cs == nil || rec.State != delegation.StateActive || rec.ChildURL == "" || rec.ChildKey == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, dup := cs.running[rec.ChildOrigin]; dup {
		return
	}
	if cs.monitor != nil {
		cs.monitor.Add(rec.ChildOrigin, rec.ChildURL)
	}

	child, cancel := context.WithCancel(ctx)

	w := &witness.Witness{
		Store: cs.store,
		// The child serves its checkpoints under /dedi, same as any node.
		TargetURL: strings.TrimRight(rec.ChildURL, "/") + "/dedi",
		TargetKey: rec.ChildKey,
		Origin:    rec.ChildOrigin,
		Interval:  cs.iv,

		RecordInterval: cs.record,
	}
	if cs.cluster != nil {
		// Verdicts are log entries, so they take the leader path like every
		// other write; followers stand by rather than all three polling the
		// child to learn the same fact.
		w.Writer = cs.cluster
		w.IsWriter = cs.cluster.IsLeader
	}
	cs.running[rec.ChildOrigin] = &childWitness{w: w, cancel: cancel}
	go w.Run(child)
	log.Printf("delegation: witnessing child %s at %s every %s", rec.ChildOrigin, rec.ChildURL, cs.iv)
}

// Resume restarts witnessing for every child already delegated, so a restart
// does not silently stop checking children enrolled before it.
func (cs *childSupervisor) Resume(ctx context.Context, namespaces []string) {
	if cs == nil {
		return
	}
	for _, ns := range namespaces {
		rows, _, err := cs.store.QueryRecords(ctx, ns, delegation.Registry, store.QueryFilters{})
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			log.Printf("delegation: could not list children of %s: %v", ns, err)
			continue
		}
		for _, row := range rows {
			e, err := cs.store.Resolve(ctx, "record", ns, delegation.Registry, row.Name, nil, nil)
			if err != nil {
				continue
			}
			rec, err := delegation.ParseRecord(e.PayloadRaw)
			if err != nil {
				continue
			}
			cs.Start(ctx, rec)
		}
	}
}

// enrolIfChild claims a delegation offer when this node was deployed as a
// child. Does nothing on a node with no offer, which is every unparented node.
//
// It runs in the background rather than blocking startup. A child that could
// not reach its parent yet is still a working directory for everything else it
// serves, and refusing to start would turn a transient DNS delay into an
// outage.
func enrolIfChild(ctx context.Context, w interface {
	Append(context.Context, store.AppendInput) (store.Entry, error)
}, st *store.Store, origin, vkey string) {
	token, ns := os.Getenv("DEDI_ENROL_TOKEN"), os.Getenv("DEDI_ENROL_NAMESPACE")
	parent := strings.TrimRight(os.Getenv("DEDI_PARENT_URL"), "/")
	if token == "" || ns == "" || parent == "" {
		return
	}
	self := strings.TrimRight(os.Getenv("DEDI_PUBLIC_URL"), "/")
	if self == "" {
		log.Printf("delegation: DEDI_PUBLIC_URL is not set — the parent needs a reachable URL to " +
			"witness this node, so enrolment is being skipped rather than registering an address " +
			"nobody can reach")
		return
	}
	c := &delegation.Client{
		ParentURL: parent, Namespace: ns, Token: token,
		Origin: origin, Key: vkey, SelfURL: self,
		ParentKey: os.Getenv("DEDI_PARENT_KEY"),
	}
	// Create the delegated namespace in this node's own log, recording who
	// granted it. Without this the child is enrolled and unable to publish
	// anything, because every write under the namespace fails on a missing
	// parent entry — and it took running a real child against a real parent to
	// notice, since every health surface reads fine.
	c.OnEnrolled = func(ctx context.Context) error {
		if _, err := st.Resolve(ctx, "namespace", ns, "", "", nil, nil); err == nil {
			return nil // already there, e.g. this is a restart
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"description":  "namespace delegated by " + parent,
			"delegated_by": parent,
			"parent_key":   c.ParentKey,
			"delegated_at": time.Now().UTC().Format(time.RFC3339),
		})
		_, err := w.Append(ctx, store.AppendInput{
			EntryType: "namespace", Namespace: ns, PayloadRaw: payload, CreatedBy: "delegation",
		})
		return err
	}
	go c.Run(ctx)
}

// Health reports the liveness of the loop watching one child, and whether there
// is a loop at all.
//
// The second return matters as much as the first: a child the log lists as
// active with no loop running is the failure this is here to make visible —
// usually a child enrolled before a restart that Resume did not pick up, which
// otherwise shows only as a verdict frozen at whatever it last said.
func (cs *childSupervisor) Health(origin string) (witness.Health, bool) {
	if cs == nil {
		return witness.Health{}, false
	}
	cs.mu.Lock()
	cw, running := cs.running[origin]
	cs.mu.Unlock()
	if !running {
		return witness.Health{}, false
	}
	return cw.w.Status(), true
}

// storeProposer lets an unreplicated node use the same delivery loop as a
// clustered one, rather than the loop branching on whether this deployment
// happens to have peers.
type storeProposer struct{ s *store.Store }

func (p storeProposer) Webhook(ctx context.Context, c store.WebhookCommand) error {
	return p.s.ApplyWebhook(ctx, c)
}

// notifyingAppender wakes the webhook loop after a successful append.
//
// Polling alone bounds the exposure window rather than closing it: measured on
// a real node with a five second sweep, a revocation reached its consumer in
// 0.6 to 4.7 seconds. Better than the fifteen the TTL gave, and the same shape
// of answer — which is not what the ticket claims push does. Waking on the
// write is what actually closes it.
//
// It wraps the writer rather than living inside the store because only the
// leader delivers, and only the leader's writes come through here. A follower
// applying replicated entries must not fire deliveries.
type notifyingAppender struct {
	inner  api.Appender
	notify func()
}

func (n notifyingAppender) Append(ctx context.Context, in store.AppendInput) (store.Entry, error) {
	e, err := n.inner.Append(ctx, in)
	if err == nil {
		n.notify()
	}
	return e, err
}

// Webhook forwards subscription changes to whatever the wrapped writer is.
// Without this the wrapper would silently hide the cluster's proposer from the
// API, and every subscription would be written to the leader's own database
// only — the exact failure replicating them was meant to fix.
//
// On a standalone node the wrapped writer is the store itself, which applies
// the change directly. This used to return nil for it, so on every unclustered
// node a subscription was acknowledged and never written, and the handler's
// read-back answered 500. Any other writer is refused rather than reported as
// done: a subscription change that went nowhere must not look like success.
func (n notifyingAppender) Webhook(ctx context.Context, c store.WebhookCommand) error {
	switch w := n.inner.(type) {
	case interface {
		Webhook(context.Context, store.WebhookCommand) error
	}:
		return w.Webhook(ctx, c)
	case *store.Store:
		return w.ApplyWebhook(ctx, c)
	default:
		return fmt.Errorf("subscription change %q: this node's writer cannot apply it", c.Op)
	}
}
