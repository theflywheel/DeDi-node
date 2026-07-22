package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SaveAnchor records that the checkpoint at treeSize was anchored to an
// external ledger backend. Idempotent per (backend, tree_size).
func (s *Store) SaveAnchor(ctx context.Context, backend string, treeSize int64, txRef, blockRef string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO anchors (backend, tree_size, tx_ref, block_ref) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (backend, tree_size) DO NOTHING`, backend, treeSize, txRef, blockRef)
	return err
}

// LastAnchoredSize returns the largest tree size already anchored to backend,
// or 0 if none.
func (s *Store) LastAnchoredSize(ctx context.Context, backend string) (int64, error) {
	var size int64
	err := s.pool.QueryRow(ctx,
		`SELECT tree_size FROM anchors WHERE backend=$1 ORDER BY tree_size DESC LIMIT 1`, backend).Scan(&size)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return size, err
}
