package witness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

// fakeTarget is a log held in memory, so a test can grow it, rewrite a leaf
// or cut it short and serve an honestly signed checkpoint and an honest
// consistency proof over whatever it now holds. Honest proofs over a dishonest
// history are the case a witness exists for: every individual answer checks
// out, and only the comparison with what it saw before does not.
type fakeTarget struct {
	mu     sync.Mutex
	skey   string
	leaves [][]byte
	olds   []int64 // the old size of every consistency proof asked for
}

func (f *fakeTarget) grow(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		f.leaves = append(f.leaves, []byte(fmt.Sprintf("leaf %d", len(f.leaves))))
	}
}

func (f *fakeTarget) rewrite(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves[i] = []byte(fmt.Sprintf("rewritten %d", i))
}

func (f *fakeTarget) truncate(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves = f.leaves[:n]
}

func (f *fakeTarget) reader() tlog.HashReader {
	var hs []tlog.Hash
	r := tlog.HashReaderFunc(func(idx []int64) ([]tlog.Hash, error) {
		out := make([]tlog.Hash, len(idx))
		for i, x := range idx {
			out[i] = hs[x]
		}
		return out, nil
	})
	for i, l := range f.leaves {
		h, err := tlog.StoredHashes(int64(i), l, r)
		if err != nil {
			panic(err)
		}
		hs = append(hs, h...)
	}
	return r
}

func (f *fakeTarget) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dedi/log/checkpoint", func(wr http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := int64(len(f.leaves))
		root, err := tlog.TreeHash(n, f.reader())
		if err != nil {
			http.Error(wr, err.Error(), 500)
			return
		}
		note, err := merkle.SignCheckpoint(f.skey, "target.test/log", n, root)
		if err != nil {
			http.Error(wr, err.Error(), 500)
			return
		}
		wr.Write([]byte(note))
	})
	mux.HandleFunc("GET /dedi/log/proof/consistency", func(wr http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		old, _ := strconv.ParseInt(r.URL.Query().Get("old"), 10, 64)
		n, _ := strconv.ParseInt(r.URL.Query().Get("new"), 10, 64)
		f.olds = append(f.olds, old)
		proof, err := tlog.ProveTree(n, old, f.reader())
		if err != nil {
			http.Error(wr, err.Error(), 400)
			return
		}
		out := make([]string, len(proof))
		for i, h := range proof {
			out[i] = base64.StdEncoding.EncodeToString(h[:])
		}
		json.NewEncoder(wr).Encode(map[string]any{"data": map[string]any{"proof": out}})
	})
	return mux
}

// askedFrom returns the old sizes of the proofs asked for since it was last
// called, as text so a test can compare it in one line.
func (f *fakeTarget) askedFrom() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := fmt.Sprint(f.olds)
	f.olds = nil
	return s
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// recordingWitness is a witness of a fakeTarget that writes at most one
// consistent verdict an hour, on a clock the test moves by hand.
func recordingWitness(t *testing.T) (*Witness, *fakeTarget, *fakeClock) {
	t.Helper()
	s, _, _, vkey, skey := setup(t)
	f := &fakeTarget{skey: skey}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	clk := &fakeClock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	w := &Witness{Store: s, TargetURL: srv.URL + "/dedi", TargetKey: vkey, Origin: "target.test",
		Interval: time.Minute, RecordInterval: time.Hour, Client: srv.Client(), now: clk.now}
	return w, f, clk
}

type verdict struct {
	Version       int32
	State         string
	Size          int64
	ConsistencyOK bool
}

func latestVerdict(t *testing.T, w *Witness) verdict {
	t.Helper()
	e, err := w.Store.Resolve(context.Background(), "record", witnessNS, w.Origin, "checkpoint", nil, nil)
	if err != nil {
		t.Fatalf("no verdict: %v", err)
	}
	var p struct {
		Size          int64 `json:"size"`
		ConsistencyOK bool  `json:"consistency_ok"`
	}
	if err := json.Unmarshal(e.PayloadRaw, &p); err != nil {
		t.Fatal(err)
	}
	return verdict{e.VersionNum, e.State, p.Size, p.ConsistencyOK}
}

// A target that grows on every check is the ring's steady state: each
// witness's own verdicts are what its watcher sees growing. Recording one
// verdict per check is what made 22,001 of 22,043 entries on the public node
// verdicts. It must be one per RecordInterval, while health still moves on
// every check, because health is what says the witness is alive.
func TestConsistentGrowthIsRecordedOncePerRecordInterval(t *testing.T) {
	w, f, clk := recordingWitness(t)
	ctx := context.Background()

	f.grow(3)
	w.check(ctx)
	first := latestVerdict(t, w)
	if first.Size != 3 || !first.ConsistencyOK || first.State != "live" {
		t.Fatalf("first verdict: %+v", first)
	}

	for i := 1; i <= 5; i++ {
		clk.advance(time.Minute)
		f.grow(1)
		w.check(ctx)
		if v := latestVerdict(t, w); v.Version != first.Version {
			t.Fatalf("check %d appended a verdict inside the record interval: %+v", i, v)
		}
		h := w.Status()
		if h.Attempts != int64(i+1) || h.Failures != 0 || !h.LastSuccessAt.Equal(clk.t) {
			t.Fatalf("check %d did not refresh health: %+v (now %s)", i, h, clk.t)
		}
	}

	// An hour after the first verdict the tree it has verified since is
	// written down, even though nothing grew on this particular check.
	clk.advance(55 * time.Minute)
	w.check(ctx)
	v := latestVerdict(t, w)
	if v.Version != first.Version+1 || v.Size != 8 || !v.ConsistencyOK {
		t.Fatalf("after the record interval: %+v, want one new verdict at size 8", v)
	}

	// And then nothing more until there is something new to say.
	clk.advance(2 * time.Hour)
	w.check(ctx)
	if again := latestVerdict(t, w); again.Version != v.Version {
		t.Fatalf("an unchanged target got another verdict: %+v", again)
	}
}

// Holding back "ok" verdicts must never hold back an alarm.
func TestAlarmsInsideTheRecordIntervalAreRecordedAtOnce(t *testing.T) {
	for name, tamper := range map[string]func(*fakeTarget){
		// Same size, different root: a leaf swapped in place.
		"equivocation": func(f *fakeTarget) { f.rewrite(5) },
		// Grown, but not from what was there.
		"fork": func(f *fakeTarget) { f.rewrite(1); f.grow(2) },
		// Smaller than a tree already seen.
		"shrink": func(f *fakeTarget) { f.truncate(5) },
	} {
		t.Run(name, func(t *testing.T) {
			w, f, clk := recordingWitness(t)
			ctx := context.Background()
			f.grow(4)
			w.check(ctx)
			before := latestVerdict(t, w)

			clk.advance(time.Minute)
			f.grow(2)
			w.check(ctx) // seen at 6, not recorded

			clk.advance(time.Minute)
			tamper(f)
			r, err := w.VerifyOnce(ctx)
			if err != nil {
				t.Fatalf("want a recorded alarm, got an error: %v", err)
			}
			v := latestVerdict(t, w)
			if r.ConsistencyOK || !r.Fresh || v.Version != before.Version+1 || v.State != "revoked" || v.ConsistencyOK {
				t.Fatalf("alarm not recorded at once: result %+v, verdict %+v", r, v)
			}
		})
	}
}

// Between recorded verdicts the witness proves consistency from the last tree
// it VERIFIED, not the last one it wrote down. From the written one, a rewrite
// of an entry that arrived after it would pass: the prefix the log verdict
// covers is untouched, so the proof from there is perfectly good.
func TestConsistencyIsProvedFromTheLastTreeSeen(t *testing.T) {
	t.Run("asks from the last tree seen", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3) // A, recorded
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2) // B, seen only
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2) // C
		w.check(ctx)
		if got := f.askedFrom(); got != "[3 5]" {
			t.Fatalf("proofs asked from sizes %s, want [3 5]: each from the last tree seen, not the last recorded", got)
		}
		if v := latestVerdict(t, w); v.Size != 3 {
			t.Fatalf("a verdict was recorded inside the interval: %+v", v)
		}
	})

	t.Run("catches a rewrite between the recorded and the seen tree", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3) // A, recorded
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2) // B, seen only
		w.check(ctx)
		clk.advance(time.Minute)
		f.rewrite(4) // inside B, outside A
		f.grow(2)
		r, err := w.VerifyOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r.ConsistencyOK {
			t.Fatal("a rewrite of an entry seen but not yet recorded passed")
		}
		if v := latestVerdict(t, w); v.State != "revoked" {
			t.Fatalf("verdict %+v, want the alarm", v)
		}
	})

	// A restart loses the in-memory tree; the log's newest verdict is all
	// there is, as it was before verdicts were held back.
	t.Run("after a restart, from the last recorded verdict", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3)
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2)
		w.check(ctx)
		f.askedFrom()

		restarted := &Witness{Store: w.Store, TargetURL: w.TargetURL, TargetKey: w.TargetKey, Origin: w.Origin,
			Interval: w.Interval, RecordInterval: w.RecordInterval, Client: w.Client, now: clk.now}
		f.grow(1)
		if _, err := restarted.VerifyOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.askedFrom(); got != "[3]" {
			t.Fatalf("after a restart proofs were asked from %s, want [3], the recorded verdict", got)
		}
	})

	// A replica that stood by while another led may hold a tree older than
	// the verdicts the leader wrote meanwhile, so on leading again it starts
	// from the log, exactly as after a restart.
	t.Run("after standing by, from the last recorded verdict", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		leading := true
		w.IsWriter = func() bool { return leading }
		f.grow(3)
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2)
		w.check(ctx)
		leading = false
		w.check(ctx)
		leading = true
		f.askedFrom()
		f.grow(1)
		w.check(ctx)
		if got := f.askedFrom(); got != "[3]" {
			t.Fatalf("after standing by proofs were asked from %s, want [3], the recorded verdict", got)
		}
	})
}
