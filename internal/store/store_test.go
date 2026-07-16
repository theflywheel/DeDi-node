package store

import (
	"context"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 applied migrations, got %d", n)
	}
}
