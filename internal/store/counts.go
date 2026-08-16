package store

import "context"

// AddRequestCounts folds a batch of per-class deltas into request_counts. The
// UPDATE is additive rather than a set, so concurrent flushes from several node
// replicas accumulate instead of clobbering each other. Zero deltas are skipped.
func (s *Store) AddRequestCounts(ctx context.Context, deltas map[string]int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for class, d := range deltas {
		if d == 0 {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO request_counts (class, n) VALUES ($1,$2)
			 ON CONFLICT (class) DO UPDATE SET n = request_counts.n + EXCLUDED.n, updated_at = now()`,
			class, d); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RequestCounts returns the persisted per-class totals. Counts accumulated
// since the last flush live in memory and are added by the caller.
func (s *Store) RequestCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT class, n FROM request_counts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var class string
		var n int64
		if err := rows.Scan(&class, &n); err != nil {
			return nil, err
		}
		out[class] = n
	}
	return out, rows.Err()
}
