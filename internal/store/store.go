// Package store is the append-only log-backed storage layer for dedid.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrNoCheckpoint    = errors.New("no checkpoint published yet")
	ErrVersionConflict = errors.New("version conflict")
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dbURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }
