package store

import (
	"context"

	"golang.org/x/mod/sumdb/tlog"
)

// ProveInclusion proves the leaf at seq is in the tree of treeSize leaves.
func (s *Store) ProveInclusion(ctx context.Context, treeSize, seq int64) (tlog.RecordProof, error) {
	return tlog.ProveRecord(treeSize, seq, hashReader{ctx, s.pool})
}

// ProveConsistency proves the tree of oldSize leaves is a prefix of the tree
// of newSize leaves.
func (s *Store) ProveConsistency(ctx context.Context, oldSize, newSize int64) (tlog.TreeProof, error) {
	return tlog.ProveTree(newSize, oldSize, hashReader{ctx, s.pool})
}
