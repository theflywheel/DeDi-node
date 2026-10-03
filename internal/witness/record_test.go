package witness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
	"github.com/theflywheel/DeDi-node/internal/store"
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

func (f *fakeTarget) restore(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves[i] = []byte(fmt.Sprintf("leaf %d", i))
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

// twin is a second witness of the same target writing to the same log, with
// none of w's memory: another replica that led for a while, a restart, or a
// peer witness and a child loop given the same origin.
func twin(w *Witness) *Witness {
	return &Witness{Store: w.Store, TargetURL: w.TargetURL, TargetKey: w.TargetKey, Origin: w.Origin,
		Interval: w.Interval, RecordInterval: w.RecordInterval, Client: w.Client, now: w.now}
}

// history is every verdict in the log, oldest first, minus the target URL,
// which differs between test servers.
func history(t *testing.T, w *Witness) []string {
	t.Helper()
	es, err := w.Store.Versions(context.Background(), "record", witnessNS, w.Origin, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		var p map[string]any
		if err := json.Unmarshal(e.PayloadRaw, &p); err != nil {
			t.Fatal(err)
		}
		delete(p, "target")
		b, _ := json.Marshal(p)
		out = append(out, e.State+" "+string(b))
	}
	return out
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

// Holding back "ok" verdicts must never hold back an alarm, and an alarm is
// raised once, not on every check while the target keeps serving the tree.
func TestAlarmsInsideTheRecordIntervalAreRecordedAtOnce(t *testing.T) {
	for name, tamper := range map[string]func(*fakeTarget){
		// Same size, different root: a leaf swapped in place, after the
		// logged tree, so only the proof from the tree seen since catches it.
		"equivocation": func(f *fakeTarget) { f.rewrite(5) },
		// Grown, but not from what was logged.
		"fork": func(f *fakeTarget) { f.rewrite(1); f.grow(2) },
		// Smaller than a tree already seen, though not than the logged one.
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
			w.check(ctx) // seen at 6, not logged

			clk.advance(time.Minute)
			tamper(f)
			r, err := w.VerifyOnce(ctx)
			if err != nil {
				t.Fatalf("want a recorded alarm, got an error: %v", err)
			}
			v := latestVerdict(t, w)
			if r.ConsistencyOK || !r.Fresh || v.Version <= before.Version || v.State != "revoked" || v.ConsistencyOK {
				t.Fatalf("alarm not recorded at once: result %+v, verdict %+v", r, v)
			}

			clk.advance(time.Minute)
			if _, err := w.VerifyOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if again := latestVerdict(t, w); again.Version != v.Version {
				t.Fatalf("the same tampered tree alarmed again: %+v after %+v", again, v)
			}
		})
	}
}

// Between logged verdicts the witness also proves consistency from the last
// tree it verified. From the logged tree alone, a rewrite of an entry that
// arrived after it would pass: the prefix the logged verdict covers is
// untouched, so the proof from there is perfectly good.
func TestConsistencyIsAlsoProvedFromTheLastTreeSeen(t *testing.T) {
	t.Run("asks from the logged tree and the tree seen", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3) // logged
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2) // seen only
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2)
		w.check(ctx)
		if got := f.askedFrom(); got != "[3 3 5]" {
			t.Fatalf("proofs asked from sizes %s, want [3 3 5]: 3->5, then 3->7 and 5->7", got)
		}
		if v := latestVerdict(t, w); v.Size != 3 {
			t.Fatalf("a verdict was logged inside the interval: %+v", v)
		}
	})

	// The alarm must be checkable by someone holding nothing but the log and
	// the target's own answers. The tree it contradicts was never logged, so
	// the witness logs it (as consistent, which it was proven to be) before
	// the alarm. A reader then takes the two newest verdicts, asks the target
	// for the proof between them, and sees it fail.
	t.Run("logs the tree an alarm contradicts, so a reader can re-check it", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3) // logged
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2) // seen only
		w.check(ctx)
		clk.advance(time.Minute)
		f.rewrite(4) // inside the seen tree, outside the logged one
		f.grow(2)
		r, err := w.VerifyOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r.ConsistencyOK {
			t.Fatal("a rewrite of an entry seen but not yet logged passed")
		}

		es, err := w.Store.Versions(ctx, "record", witnessNS, w.Origin, "checkpoint")
		if err != nil || len(es) < 2 {
			t.Fatalf("verdicts: %d, %v", len(es), err)
		}
		type rec struct {
			Size          int64  `json:"size"`
			Root          string `json:"root"`
			ConsistencyOK bool   `json:"consistency_ok"`
		}
		var prev, alarm rec
		json.Unmarshal(es[len(es)-2].PayloadRaw, &prev)
		json.Unmarshal(es[len(es)-1].PayloadRaw, &alarm)
		if !prev.ConsistencyOK || prev.Size != 5 || alarm.ConsistencyOK || alarm.Size != 7 {
			t.Fatalf("want ok(5) then the alarm at 7, got %+v then %+v", prev, alarm)
		}
		hash := func(s string) (h tlog.Hash) {
			b, _ := base64.StdEncoding.DecodeString(s)
			copy(h[:], b)
			return h
		}
		reader := &Witness{TargetURL: w.TargetURL, Client: w.Client}
		proof, err := reader.fetchConsistency(ctx, prev.Size, alarm.Size)
		if err != nil {
			t.Fatal(err)
		}
		if tlog.CheckTree(proof, alarm.Size, hash(alarm.Root), prev.Size, hash(prev.Root)) == nil {
			t.Fatal("from the log alone the alarm cannot be re-checked: the two newest verdicts are consistent")
		}
	})

	// A restart loses the tree seen; the newest logged verdict is all there
	// is, as it was before verdicts were held back.
	t.Run("after a restart, from the logged verdict alone", func(t *testing.T) {
		w, f, clk := recordingWitness(t)
		ctx := context.Background()
		f.grow(3)
		w.check(ctx)
		clk.advance(time.Minute)
		f.grow(2)
		w.check(ctx)
		f.askedFrom()

		f.grow(1)
		if _, err := twin(w).VerifyOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.askedFrom(); got != "[3]" {
			t.Fatalf("after a restart proofs were asked from %s, want [3], the logged verdict", got)
		}
	})
}

// The log, not a witness's memory, is the authority on what was verified.
// Two witnesses take turns on one log here: a leader that stepped down and
// back between two of its own checks, or a peer witness and a child loop
// given the same origin. Each must judge the target against the newest
// verdict either of them logged; one that kept proving from its own older
// tree would log a verdict that contradicts the other's and call it ok.
func TestAWitnessDefersToVerdictsAnotherWitnessLogged(t *testing.T) {
	for name, tamper := range map[string]func(*fakeTarget){
		// ok(7) logged by the other witness, then the target serves 6.
		"shrink": func(f *fakeTarget) { f.truncate(6) },
		// ok(7) was logged over a rewritten leaf; the target puts it back.
		"fork": func(f *fakeTarget) { f.restore(4); f.grow(1) },
	} {
		t.Run(name, func(t *testing.T) {
			a, f, clk := recordingWitness(t)
			b := twin(a)
			ctx := context.Background()
			f.grow(3)
			a.check(ctx) // a logs ok(3)
			clk.advance(time.Minute)
			f.grow(2)
			a.check(ctx) // a has seen 5, logged nothing

			clk.advance(time.Minute)
			if name == "fork" {
				f.rewrite(4)
			}
			f.grow(2)
			b.check(ctx) // b logs ok(7), proven from 3
			if v := latestVerdict(t, a); v.Size != 7 || !v.ConsistencyOK {
				t.Fatalf("setup: the other witness did not log ok(7): %+v", v)
			}

			clk.advance(time.Minute)
			tamper(f)
			r, err := a.VerifyOnce(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if v := latestVerdict(t, a); r.ConsistencyOK || v.State != "revoked" {
				t.Fatalf("a contradiction of the other witness's verdict was not alarmed: %+v\n%v", v, history(t, a))
			}
		})
	}
}

// The tree a witness saw but did not log was proven against the verdict that
// was newest at the time. Once another witness logs over it, nothing says the
// two agree, so logging the remembered tree as "ok" (as the alarm path does,
// to put evidence in the log) could put a false ok in the chain. Here it
// would also be smaller than the verdict before it.
//
// The rewrite of leaf 4 goes unalarmed by a as a result. That is the
// documented loss of a witness's unlogged span on a leader change.
func TestAWitnessNeverLogsOkForATreeTheNewestVerdictWasNotProvenAgainst(t *testing.T) {
	a, f, clk := recordingWitness(t)
	b := twin(a)
	ctx := context.Background()
	f.grow(3)
	a.check(ctx) // a logs ok(3)
	clk.advance(time.Minute)
	f.grow(2)
	a.check(ctx) // a has seen 5
	clk.advance(time.Minute)
	f.rewrite(4)
	f.grow(2)
	b.check(ctx) // b logs ok(7), proven from 3; 7 and a's 5 disagree
	clk.advance(time.Minute)
	f.grow(1)
	if _, err := a.VerifyOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, v := range history(t, a)[2:] {
		if strings.Contains(v, `"size":5`) {
			t.Fatalf("a logged the tree it saw before the other witness's verdict: %v", history(t, a))
		}
	}
}

// With RecordInterval zero the witness must do exactly what it did before
// RecordInterval existed. upstreamVerifyOnce below is that code, copied from
// upstream/main at 4ee80d4; the same scenario runs against each and the logs
// they leave must match verdict for verdict.
func TestZeroRecordIntervalLogsExactlyWhatTheOldWitnessDid(t *testing.T) {
	run := func(verify func(*Witness, context.Context) (Result, error)) ([]string, []Result) {
		a, f, _ := recordingWitness(t)
		a.RecordInterval = 0
		b := twin(a)
		ctx := context.Background()
		var results []Result
		step := func(w *Witness, change func()) {
			change()
			r, err := verify(w, ctx)
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, r)
		}
		step(a, func() { f.grow(3) })
		step(a, func() { f.grow(2) })
		step(a, func() {})
		step(b, func() { f.grow(2) })
		step(a, func() { f.truncate(6) }) // shrink
		step(a, func() {})
		step(b, func() { f.grow(2) })
		step(a, func() { f.rewrite(7) }) // equivocation
		step(b, func() {})
		step(a, func() { f.rewrite(2); f.grow(2) }) // fork
		step(b, func() { f.grow(1) })
		step(a, func() { f.restore(2) }) // equivocation back
		return history(t, a), results
	}
	oldLog, oldRes := run(upstreamVerifyOnce)
	newLog, newRes := run((*Witness).VerifyOnce)
	if fmt.Sprint(oldLog) != fmt.Sprint(newLog) {
		t.Fatalf("logs differ\nold:\n%s\nnew:\n%s", strings.Join(oldLog, "\n"), strings.Join(newLog, "\n"))
	}
	if fmt.Sprint(oldRes) != fmt.Sprint(newRes) {
		t.Fatalf("results differ\nold: %+v\nnew: %+v", oldRes, newRes)
	}
	if len(oldLog) < 8 {
		t.Fatalf("the scenario logged only %d verdicts; it has stopped exercising anything", len(oldLog))
	}
}

func upstreamVerifyOnce(w *Witness, ctx context.Context) (Result, error) {
	size, root, err := w.verifyCheckpoint(ctx)
	if err != nil {
		return Result{}, err
	}
	rootB64 := base64.StdEncoding.EncodeToString(root[:])
	if err := w.ensureParents(ctx); err != nil {
		return Result{}, err
	}
	last, lastRoot, have := w.lastWitnessed(ctx)
	if have && size == last {
		if root == lastRoot {
			return Result{Size: size, Root: rootB64, ConsistencyOK: true, Fresh: false}, nil
		}
		payload, _ := json.Marshal(map[string]any{
			"target": w.TargetURL, "size": size, "root": rootB64, "consistency_ok": false,
			"detail": "root changed while tree size stayed at " + fmt.Sprint(size) +
				": history was rewritten in place",
		})
		if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "record", Namespace: witnessNS,
			Registry: w.Origin, RecordName: "checkpoint", PayloadRaw: payload, State: "revoked",
			CreatedBy: "witness"}); err != nil {
			return Result{}, err
		}
		return Result{Size: size, Root: rootB64, ConsistencyOK: false, Fresh: true}, nil
	}
	consistencyOK := true
	if have && last > 0 {
		if size < last {
			consistencyOK = false
		} else {
			proof, err := w.fetchConsistency(ctx, last, size)
			if err != nil {
				return Result{}, err
			}
			if err := tlog.CheckTree(proof, size, root, last, lastRoot); err != nil {
				consistencyOK = false
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"target": w.TargetURL, "size": size, "root": rootB64, "consistency_ok": consistencyOK,
	})
	state := "live"
	if !consistencyOK {
		state = "revoked"
	}
	if _, err := w.appender().Append(ctx, store.AppendInput{EntryType: "record", Namespace: witnessNS, Registry: w.Origin,
		RecordName: "checkpoint", PayloadRaw: payload, State: state, CreatedBy: "witness"}); err != nil {
		return Result{}, err
	}
	return Result{Size: size, Root: rootB64, ConsistencyOK: consistencyOK, Fresh: true}, nil
}
