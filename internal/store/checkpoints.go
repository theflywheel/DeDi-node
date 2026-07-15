package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) SaveCheckpoint(ctx context.Context, size int64, root []byte, noteText string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checkpoints (tree_size, root_hash, note_text) VALUES ($1,$2,$3)
		 ON CONFLICT (tree_size) DO NOTHING`, size, root, noteText)
	return err
}

func (s *Store) LatestCheckpoint(ctx context.Context) (int64, string, error) {
	var size int64
	var text string
	err := s.pool.QueryRow(ctx,
		`SELECT tree_size, note_text FROM checkpoints ORDER BY tree_size DESC LIMIT 1`).Scan(&size, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNoCheckpoint
	}
	return size, text, err
}

// TruncateForTest empties all log state. Test support only.
func (s *Store) TruncateForTest(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE log_entries, tree_hashes, checkpoints`)
	return err
}
