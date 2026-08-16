package store

import (
	"context"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/testdb"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := testdb.URL(t)
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}
