package checkpoint

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/merkle"
	"github.com/theflywheel/DeDi-node/internal/store"

	"github.com/theflywheel/DeDi-node/internal/testdb"
)

func testCheckpointer(t *testing.T) (*Checkpointer, *store.Store, string) {
	t.Helper()
	url := testdb.URL(t)
	ctx := context.Background()
	s, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	skey, vkey, err := note.GenerateKey(rand.Reader, "test.dedi.local")
	if err != nil {
		t.Fatal(err)
	}
	return &Checkpointer{Store: s, SKey: skey, Origin: "test.dedi.local/log", Interval: time.Hour}, s, vkey
}

func TestPublishNowSignsCurrentTree(t *testing.T) {
	cp, s, vkey := testCheckpointer(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	size, text, err := cp.PublishNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if size != 1 {
		t.Fatalf("size=%d want 1", size)
	}
	verifier, err := note.NewVerifier(vkey)
	if err != nil {
		t.Fatal(err)
	}
	n, err := note.Open([]byte(text), note.VerifierList(verifier))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	_, psize, root, err := merkle.ParseCheckpoint(n.Text)
	if err != nil || psize != 1 {
		t.Fatalf("parse: size=%d err=%v", psize, err)
	}
	want, err := s.TreeRoot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatal("checkpoint root != tree root")
	}
	// idempotent at same size
	size2, text2, err := cp.PublishNow(ctx)
	if err != nil || size2 != 1 || text2 != text {
		t.Fatalf("second publish changed checkpoint: %v", err)
	}
}

func TestPublishNowEmptyTree(t *testing.T) {
	cp, _, _ := testCheckpointer(t)
	size, text, err := cp.PublishNow(context.Background())
	if err != nil || size != 0 || text == "" {
		t.Fatalf("empty-tree checkpoint: size=%d err=%v", size, err)
	}
}
