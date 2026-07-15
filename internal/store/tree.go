package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/mod/sumdb/tlog"
)

// rowQuerier is the subset of pgx query capability hashReader needs;
// both *pgxpool.Pool and pgx.Tx satisfy it.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

var (
	_ rowQuerier = (*pgxpool.Pool)(nil)
	_ rowQuerier = (pgx.Tx)(nil)
)

// hashReader serves stored Merkle hashes to sumdb/tlog.
type hashReader struct {
	ctx context.Context
	q   rowQuerier
}

func (r hashReader) ReadHashes(indexes []int64) ([]tlog.Hash, error) {
	rows, err := r.q.Query(r.ctx, `SELECT idx, hash FROM tree_hashes WHERE idx = ANY($1)`, indexes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[int64]tlog.Hash, len(indexes))
	for rows.Next() {
		var idx int64
		var hb []byte
		if err := rows.Scan(&idx, &hb); err != nil {
			return nil, err
		}
		var h tlog.Hash
		copy(h[:], hb)
		found[idx] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]tlog.Hash, len(indexes))
	for i, idx := range indexes {
		h, ok := found[idx]
		if !ok {
			return nil, fmt.Errorf("missing tree hash at index %d", idx)
		}
		out[i] = h
	}
	return out, nil
}

// TreeSize is the number of leaves in the log.
func (s *Store) TreeSize(ctx context.Context) (int64, error) {
	var size int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq)+1, 0) FROM log_entries`).Scan(&size)
	return size, err
}

// TreeRoot computes the RFC 6962 root of the first size leaves.
func (s *Store) TreeRoot(ctx context.Context, size int64) (tlog.Hash, error) {
	return tlog.TreeHash(size, hashReader{ctx, s.pool})
}
