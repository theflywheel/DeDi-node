// Package store is the append-only log-backed storage layer for dedid.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrNoCheckpoint = errors.New("no checkpoint published yet")
	// ErrInvalidWrite marks an append rejected for the caller's reasons —
	// wrong shape, bad payload — as opposed to a node failure. Callers use it
	// to answer 400 rather than 500.
	ErrInvalidWrite = errors.New("invalid write")
	// ErrVersionConflict marks an append whose caller-supplied precondition no
	// longer holds: the resource moved on since the caller read it.
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

// Ping checks the database is actually reachable. Open pings once at startup,
// but a pool that was healthy then can be unreachable now, and every read path
// would fail while the process itself keeps answering — so health probes need
// their own round trip rather than trusting the pool's existence.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
