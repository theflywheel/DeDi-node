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
	// Compare against the embedded files rather than a literal: a second
	// Migrate must not reapply anything, whatever the migration count is.
	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != len(files) {
		t.Fatalf("want %d applied migrations, got %d", len(files), n)
	}
}
