// Package checkpoint publishes signed tree heads on a cadence.
package checkpoint

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"time"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Checkpointer struct {
	Store    *store.Store
	SKey     string // sumdb/note private key
	Origin   string // checkpoint origin line
	Interval time.Duration

	// Publish persists a signed tree head. nil writes straight to the store,
	// which is what a standalone node wants. In a cluster it proposes the
	// signed note through Raft, so every replica stores the identical signed
	// bytes and none of them needs the identity key to serve a checkpoint.
	Publish func(ctx context.Context, size int64, root []byte, note string) error

	// IsWriter reports whether this process may sign. nil means yes.
	//
	// Only the leader signs. A follower that signed would be signing over its
	// own view of the tree, and any lag between replicas would produce two
	// different roots at the same size under one key — the fork that
	// store.SaveCheckpoint refuses outright. Followers serve the replicated
	// checkpoint instead, which is the same bytes either way.
	IsWriter func() bool
}

func (c *Checkpointer) publish(ctx context.Context, size int64, root []byte, note string) error {
	if c.Publish != nil {
		return c.Publish(ctx, size, root, note)
	}
	return c.Store.SaveCheckpoint(ctx, size, root, note)
}

func (c *Checkpointer) maySign() bool {
	return c.IsWriter == nil || c.IsWriter()
}

// PublishNow signs and persists a checkpoint for the current tree size.
// Idempotent: if the latest checkpoint already covers the current size it is
// returned unchanged.
// A follower returns whatever checkpoint has been replicated to it rather than
// minting one, so read paths that ask for a checkpoint keep working on every
// replica.
func (c *Checkpointer) PublishNow(ctx context.Context) (int64, string, error) {
	size, err := c.Store.TreeSize(ctx)
	if err != nil {
		return 0, "", err
	}
	lastSize, lastNote, err := c.Store.LatestCheckpoint(ctx)
	switch {
	case err == nil && lastSize == size:
		return lastSize, lastNote, nil
	case err != nil && !errors.Is(err, store.ErrNoCheckpoint):
		return 0, "", err
	}
	if !c.maySign() {
		if errors.Is(err, store.ErrNoCheckpoint) {
			return 0, "", err
		}
		// Behind the leader by a few entries: report the checkpoint actually
		// held. It is genuinely signed and genuinely verifiable, just not the
		// newest — which is exactly what a replica should say rather than
		// inventing something newer.
		return lastSize, lastNote, nil
	}
	var root tlog.Hash
	if size == 0 {
		root = tlog.Hash(sha256.Sum256(nil)) // RFC 6962 empty tree root
	} else {
		root, err = c.Store.TreeRoot(ctx, size)
		if err != nil {
			return 0, "", err
		}
	}
	text, err := merkle.SignCheckpoint(c.SKey, c.Origin, size, root)
	if err != nil {
		return 0, "", err
	}
	if err := c.publish(ctx, size, root[:], text); err != nil {
		return 0, "", err
	}
	return size, text, nil
}

// Run publishes on Interval until ctx is done.
func (c *Checkpointer) Run(ctx context.Context) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := c.PublishNow(ctx); err != nil {
				log.Printf("checkpointer: %v", err)
			}
		}
	}
}
