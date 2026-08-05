package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestEnsureIdentityStoresTheFirstKeyAndKeepsIt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	skey, vkey, err := s.EnsureIdentity(ctx, "SKEY-first", "VKEY-first")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if skey != "SKEY-first" || vkey != "VKEY-first" {
		t.Fatalf("first claim should win, got %q/%q", skey, vkey)
	}

	// A later boot generates a fresh candidate; adopting it would repudiate
	// every checkpoint already signed under the stored key.
	skey, vkey, err = s.EnsureIdentity(ctx, "SKEY-second", "VKEY-second")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if skey != "SKEY-first" || vkey != "VKEY-first" {
		t.Fatalf("stored identity must survive a later candidate, got %q/%q", skey, vkey)
	}
}

func TestEnsureIdentityAgreesUnderConcurrentBoot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Several replicas starting at once against an empty database is the normal
	// case on a platform that scales containers, not an exotic race. They must
	// converge on one identity: two nodes signing the same log under different
	// keys is a forked history to anyone watching.
	const replicas = 8
	var wg sync.WaitGroup
	got := make([]string, replicas)
	errs := make([]error, replicas)
	for i := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			skey, _, err := s.EnsureIdentity(ctx,
				fmt.Sprintf("SKEY-%d", i), fmt.Sprintf("VKEY-%d", i))
			got[i], errs[i] = skey, err
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	for i, k := range got {
		if k != got[0] {
			t.Fatalf("replicas disagreed on identity: replica 0 got %q, replica %d got %q", got[0], i, k)
		}
	}
	if got[0] == "" {
		t.Fatal("replicas agreed on an empty identity")
	}
}
