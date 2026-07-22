// Package anchor periodically publishes this node's signed checkpoint to an
// external ledger through a pluggable adapter (the Ledger interface). It is an
// optional, secondary trust layer: witnesses (internal/witness) remain the
// primary cross-verification mechanism; an anchor adds an externally-finalized
// timeline of roots, which makes split-view attacks harder.
//
// Adapter pattern: backends implement Ledger. The first adapter is CORD
// (cord.go); the interface is deliberately narrow so other backends (another
// chain, a timestamping service, another transparency log) drop in without
// touching the runner or the daemon wiring.
package anchor

import (
	"context"
	"log"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Ref identifies where an anchor landed on the target ledger.
type Ref struct {
	TxRef    string // transaction hash / identifier
	BlockRef string // block hash / height, if known
}

// Ledger is the adapter interface an anchoring backend implements.
type Ledger interface {
	// Name identifies the backend in logs and the anchors table (e.g. "cord").
	Name() string
	// Anchor publishes the checkpoint bytes and returns where they landed.
	// Implementations should verify inclusion before returning when they can.
	Anchor(ctx context.Context, checkpoint []byte) (Ref, error)
}

// Anchorer runs the periodic anchor loop.
type Anchorer struct {
	Store    *store.Store
	Ledger   Ledger
	Interval time.Duration
}

// Run anchors the latest checkpoint whenever the tree has grown past the last
// anchored size. Failures are logged and retried next tick: anchoring is
// best-effort by design and must never block the node.
func (a *Anchorer) Run(ctx context.Context) {
	t := time.NewTicker(a.Interval)
	defer t.Stop()
	for {
		if err := a.Once(ctx); err != nil {
			log.Printf("anchor(%s): %v", a.Ledger.Name(), err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once performs a single anchor attempt if the tree grew. Returns nil when
// there is nothing new to anchor.
func (a *Anchorer) Once(ctx context.Context) error {
	size, text, err := a.Store.LatestCheckpoint(ctx)
	if err != nil {
		if err == store.ErrNoCheckpoint {
			return nil // nothing published yet
		}
		return err
	}
	last, err := a.Store.LastAnchoredSize(ctx, a.Ledger.Name())
	if err != nil {
		return err
	}
	if size <= last {
		return nil // already anchored
	}
	ref, err := a.Ledger.Anchor(ctx, []byte(text))
	if err != nil {
		return err
	}
	if err := a.Store.SaveAnchor(ctx, a.Ledger.Name(), size, ref.TxRef, ref.BlockRef); err != nil {
		return err
	}
	log.Printf("anchor(%s): tree size %d -> tx %s block %s", a.Ledger.Name(), size, ref.TxRef, ref.BlockRef)
	return nil
}
