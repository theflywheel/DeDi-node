package witness

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/merkle"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func setup(t *testing.T) (*store.Store, *checkpoint.Checkpointer, *httptest.Server, string, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
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
	skey, vkey, err := note.GenerateKey(rand.Reader, "target.test")
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "target.test/log", Interval: time.Hour}
	srv := httptest.NewServer((&api.Server{Store: s, CP: cp, TTL: 300}).Handler())
	t.Cleanup(srv.Close)
	return s, cp, srv, vkey, skey
}

func seed(t *testing.T, s *store.Store, names ...string) {
	t.Helper()
	ctx := context.Background()
	_, _ = s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	_, _ = s.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	for _, n := range names {
		if _, err := s.Append(ctx, store.AppendInput{EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: n, PayloadRaw: []byte(`{"v":1}`), CreatedBy: "t"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWitnessVerifiesAppendOnly(t *testing.T) {
	s, cp, srv, vkey, _ := setup(t)
	ctx := context.Background()
	seed(t, s, "a", "b", "c")
	if _, _, err := cp.PublishNow(ctx); err != nil {
		t.Fatal(err)
	}

	w := &Witness{Store: s, TargetURL: srv.URL + "/dedi", TargetKey: vkey, Origin: "target.test", Interval: time.Hour, Client: srv.Client()}

	r1, err := w.VerifyOnce(ctx)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if !r1.Fresh || !r1.ConsistencyOK {
		t.Fatalf("baseline result: %+v", r1)
	}

	// target grows, publishes a new checkpoint
	seed(t, s, "d", "e")
	if _, _, err := cp.PublishNow(ctx); err != nil {
		t.Fatal(err)
	}
	r2, err := w.VerifyOnce(ctx)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !r2.Fresh || !r2.ConsistencyOK || r2.Size <= r1.Size {
		t.Fatalf("advance result: %+v (baseline size %d)", r2, r1.Size)
	}

	// no new checkpoint -> not fresh
	r3, err := w.VerifyOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r3.Fresh {
		t.Fatalf("expected not-fresh when checkpoint unchanged: %+v", r3)
	}

	// the witness recorded its verdict in its own log, live (consistent)
	e, err := s.Resolve(ctx, "record", "_witness", "target.test", "checkpoint", nil, nil)
	if err != nil {
		t.Fatalf("witness record missing: %v", err)
	}
	if e.State != "live" {
		t.Fatalf("witness record state %q want live", e.State)
	}
	var rec struct {
		ConsistencyOK bool  `json:"consistency_ok"`
		Size          int64 `json:"size"`
	}
	json.Unmarshal(e.PayloadRaw, &rec)
	if !rec.ConsistencyOK || rec.Size != r2.Size {
		t.Fatalf("witness record payload: %+v", rec)
	}
}

func TestWitnessRejectsBadSignature(t *testing.T) {
	s, cp, srv, _, _ := setup(t)
	ctx := context.Background()
	seed(t, s, "a")
	cp.PublishNow(ctx)
	// a verifier key for a DIFFERENT identity than the one that signed
	_, wrongVkey, _ := note.GenerateKey(rand.Reader, "attacker.test")
	w := &Witness{Store: s, TargetURL: srv.URL + "/dedi", TargetKey: wrongVkey, Origin: "target.test", Interval: time.Hour, Client: srv.Client()}
	if _, err := w.VerifyOnce(ctx); err == nil {
		t.Fatal("expected signature verification to fail against the wrong key")
	}
}

func TestWitnessDetectsForkedHistory(t *testing.T) {
	s, cp, srv, vkey, skey := setup(t)
	ctx := context.Background()
	seed(t, s, "a", "b", "c")
	cp.PublishNow(ctx)

	// legit baseline against the honest server
	w := &Witness{Store: s, TargetURL: srv.URL + "/dedi", TargetKey: vkey, Origin: "target.test", Interval: time.Hour, Client: srv.Client()}
	if _, err := w.VerifyOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// a malicious target: real consistency proofs, but a validly-signed checkpoint
	// carrying a FORKED (bogus) root at the current size — so the honest proof
	// reconciles to the real root, not the forged one.
	size, _ := s.TreeSize(ctx)
	var bogus tlog.Hash
	h := sha256.Sum256([]byte("forged"))
	copy(bogus[:], h[:])
	forged, err := merkle.SignCheckpoint(skey, "target.test/log", size, bogus)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dedi/log/checkpoint", func(wr http.ResponseWriter, r *http.Request) {
		wr.Header().Set("Content-Type", "text/plain; charset=utf-8")
		wr.Write([]byte(forged))
	})
	// proxy the consistency proof to the honest server (a real, valid proof —
	// which will NOT reconcile with the forged root).
	mux.HandleFunc("GET /dedi/log/proof/consistency", func(wr http.ResponseWriter, r *http.Request) {
		resp, err := srv.Client().Get(srv.URL + "/dedi/log/proof/consistency?old=" + r.URL.Query().Get("old") + "&new=" + r.URL.Query().Get("new"))
		if err != nil {
			wr.WriteHeader(500)
			return
		}
		defer resp.Body.Close()
		wr.WriteHeader(resp.StatusCode)
		io.Copy(wr, resp.Body)
	})
	mal := httptest.NewServer(mux)
	defer mal.Close()

	// point the witness at the malicious target; its baseline is already recorded,
	// so it will run a consistency check old->forkedSize and it must fail.
	w2 := &Witness{Store: s, TargetURL: mal.URL + "/dedi", TargetKey: vkey, Origin: "target.test", Interval: time.Hour, Client: mal.Client()}
	r, err := w2.VerifyOnce(ctx)
	if err != nil {
		t.Fatalf("forked verify returned transport error, want a recorded alarm: %v", err)
	}
	if r.ConsistencyOK {
		t.Fatal("witness accepted a forked history")
	}
	// the alarm is recorded as a revoked witness record
	e, _ := s.Resolve(ctx, "record", "_witness", "target.test", "checkpoint", nil, nil)
	if e.State != "revoked" {
		t.Fatalf("forked-history verdict state %q want revoked", e.State)
	}
}
