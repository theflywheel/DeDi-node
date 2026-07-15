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
}

// PublishNow signs and persists a checkpoint for the current tree size.
// Idempotent: if the latest checkpoint already covers the current size it is
// returned unchanged.
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
	if err := c.Store.SaveCheckpoint(ctx, size, root[:], text); err != nil {
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
