package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/testdb"
)

// These tests run a real three-replica cluster in process: real Raft, real TCP
// transport, real elections, and three separate databases. A cluster test
// against fakes would prove almost nothing — the failures worth catching here
// are convergence and leadership failures, and those only exist when replicas
// genuinely disagree about what they have applied.
//
// Each replica gets its own logical database on the same Postgres instance,
// which is also how the deployment is configured. Sharing one database between
// replicas would corrupt it immediately, so the test would rather demonstrate
// the separation than assume it.

func replicaDBs(t *testing.T, n int) []string {
	t.Helper()
	base := testdb.URL(t)
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	admin, err := pgx.Connect(context.Background(), base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(context.Background())

	var out []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("dedi_replica_%d", i)
		// DROP then CREATE: a replica must start from nothing, and leftovers from
		// a previous run would look exactly like a divergence.
		for _, stmt := range []string{
			fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name),
			fmt.Sprintf(`CREATE DATABASE %s`, name),
		} {
			if _, err := admin.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		ru := *u
		ru.Path = "/" + name
		out = append(out, ru.String())
	}
	return out
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

type replica struct {
	node  *Node
	store *store.Store
	id    string
}

func startCluster(t *testing.T, n int) []*replica {
	t.Helper()
	return startClusterAt(t, n, func() string { return freeAddr(t) })
}

// startClusterAt lets a test choose how peers address each other, so the
// hostname path the deployment actually uses can be exercised rather than only
// the IP-literal one.
func startClusterAt(t *testing.T, n int, addr func() string) []*replica {
	t.Helper()
	ctx := context.Background()
	dbs := replicaDBs(t, n)

	peers := make([]Peer, n)
	for i := range peers {
		peers[i] = Peer{
			ID:       fmt.Sprintf("r%d", i),
			RaftAddr: addr(),
			HTTPURL:  fmt.Sprintf("http://replica-%d.example", i),
		}
	}

	var out []*replica
	for i, p := range peers {
		s, err := store.Open(ctx, dbs[i])
		if err != nil {
			t.Fatalf("open store %d: %v", i, err)
		}
		t.Cleanup(s.Close)
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
		node, err := Open(Config{
			ID:      p.ID,
			DataDir: t.TempDir(),
			Peers:   peers,
			// Exactly one replica bootstraps; the others discover the
			// configuration through it, which is how a real cluster forms.
			Bootstrap:    i == 0,
			ApplyTimeout: 15 * time.Second,
			OnHalt: func(err error) {
				t.Errorf("replica %s halted: %v", p.ID, err)
			},
		}, s)
		if err != nil {
			t.Fatalf("open cluster node %d: %v", i, err)
		}
		t.Cleanup(func() { node.Close() })
		out = append(out, &replica{node: node, store: s, id: p.ID})
	}
	return out
}

func waitForLeader(t *testing.T, rs []*replica) *replica {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range rs {
			if r.node.IsLeader() {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no leader elected within 20s")
	return nil
}

// waitForRoot waits until every live replica has applied up to size and agrees
// on the root. Convergence is asynchronous by design: a follower applies after
// the leader, so polling is the honest way to assert it rather than sleeping a
// guessed interval.
func waitForRoot(t *testing.T, rs []*replica, wantSize int64) string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		agreed, root := true, ""
		for _, r := range rs {
			size, err := r.store.TreeSize(ctx)
			if err != nil || size != wantSize {
				agreed = false
				break
			}
			h, err := r.store.TreeRoot(ctx, size)
			if err != nil {
				agreed = false
				break
			}
			if root == "" {
				root = h.String()
			} else if root != h.String() {
				t.Fatalf("replicas disagree on the root at size %d: %s vs %s", wantSize, root, h.String())
			}
		}
		if agreed && root != "" {
			return root
		}
		last = root
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("replicas did not converge on size %d within 20s (last root %q)", wantSize, last)
	return ""
}

func seed(t *testing.T, leader *replica) {
	t.Helper()
	ctx := context.Background()
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t"},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t"},
	} {
		if _, err := leader.node.Append(ctx, in); err != nil {
			t.Fatalf("seed %s: %v", in.EntryType, err)
		}
	}
}

func TestClusterReplicatesTheSameTreeToEveryReplica(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	seed(t, leader)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := leader.node.Append(ctx, store.AppendInput{
			EntryType: "record", Namespace: "ns", Registry: "reg",
			RecordName: fmt.Sprintf("r%d", i),
			PayloadRaw: []byte(fmt.Sprintf(`{"i":%d}`, i)), CreatedBy: "t",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	root := waitForRoot(t, rs, 7)
	if root == "" {
		t.Fatal("no root")
	}
}

// waitForKnownLeader polls one replica until it can name the leader it should
// redirect writes to, or gives up. The bound is generous relative to how long
// propagation actually takes, so a failure here means the follower never
// learned rather than that the test was impatient.
func waitForKnownLeader(t *testing.T, r *replica) (id, httpURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if id, httpURL = r.node.Leader(); id != "" && httpURL != "" {
			return id, httpURL
		}
		time.Sleep(50 * time.Millisecond)
	}
	return id, httpURL
}

func TestFollowersRefuseWritesRatherThanForkTheLog(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)

	// The whole safety argument rests on a single writer. A follower that
	// accepted an append would be building a different tree from the leader's,
	// under the same identity — so it must refuse, and it must refuse in a way
	// the caller can act on rather than a generic error.
	for _, r := range rs {
		if r.id == leader.id {
			continue
		}
		_, err := r.node.Append(context.Background(), store.AppendInput{
			EntryType: "namespace", Namespace: "ns",
			PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t",
		})
		if !errors.Is(err, ErrNotLeader) {
			t.Fatalf("follower %s: want ErrNotLeader, got %v", r.id, err)
		}
		// And it must be able to point the caller at the leader.
		//
		// Polled, not asserted outright: waitForLeader returns the moment ONE
		// replica reports leadership, and Raft propagates that to the others
		// asynchronously, so a follower can legitimately not know the leader's
		// identity yet. Asserting instantly made this test fail intermittently
		// with `names leader ""` — a race in the test, not in the cluster.
		// What matters is that a follower converges on the answer, and quickly.
		id, httpURL := waitForKnownLeader(t, r)
		if id != leader.id {
			t.Errorf("follower %s names leader %q, want %q", r.id, id, leader.id)
		}
		if httpURL == "" {
			t.Errorf("follower %s cannot redirect: no leader URL", r.id)
		}
	}
}

func TestClusterSurvivesLosingItsLeader(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	seed(t, leader)
	waitForRoot(t, rs, 2)

	// This is the property the whole exercise is for: a replica dies and the
	// directory keeps accepting writes.
	if err := leader.node.Close(); err != nil {
		t.Logf("closing leader: %v", err)
	}
	var survivors []*replica
	for _, r := range rs {
		if r.id != leader.id {
			survivors = append(survivors, r)
		}
	}

	next := waitForLeader(t, survivors)
	if next.id == leader.id {
		t.Fatal("the dead node is still leader")
	}
	if _, err := next.node.Append(context.Background(), store.AppendInput{
		EntryType: "record", Namespace: "ns", Registry: "reg", RecordName: "after-failover",
		PayloadRaw: []byte(`{"ok":true}`), CreatedBy: "t",
	}); err != nil {
		t.Fatalf("append after failover: %v", err)
	}
	waitForRoot(t, survivors, 3)
}

func TestDeterministicRejectionsReachTheClientAndDoNotHaltReplicas(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)

	// A record with no parent registry is rejected identically by every replica,
	// so it is a state transition like any other: the client gets the error, the
	// cluster stays healthy, and the log is unchanged.
	_, err := leader.node.Append(context.Background(), store.AppendInput{
		EntryType: "record", Namespace: "nope", Registry: "nope", RecordName: "x",
		PayloadRaw: []byte(`{}`), CreatedBy: "t",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// The cluster must still work afterwards — if a rejection had halted the
	// replicas this would hang or fail.
	seed(t, leader)
	waitForRoot(t, rs, 2)
}

func TestSignedCheckpointsReplicateToEveryReplica(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	seed(t, leader)
	waitForRoot(t, rs, 2)

	ctx := context.Background()
	root, err := leader.store.TreeRoot(ctx, 2)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	const note = "example.org/log\n2\n" + "cm9vdA==\n\n— test AAAA\n"
	if err := leader.node.SignCheckpoint(ctx, 2, root[:], note); err != nil {
		t.Fatalf("sign checkpoint: %v", err)
	}

	// Followers store the leader's signed bytes verbatim. They never re-sign,
	// and so never need the identity key — which is what lets a checkpoint stay
	// verifiable no matter which replica serves it.
	deadline := time.Now().Add(20 * time.Second)
	for _, r := range rs {
		for {
			size, text, err := r.store.LatestCheckpoint(ctx)
			if err == nil && size == 2 {
				if text != note {
					t.Fatalf("replica %s stored different checkpoint bytes:\n%q\nwant\n%q", r.id, text, note)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("replica %s never received the checkpoint (%v)", r.id, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func TestParsePeers(t *testing.T) {
	ok, err := ParsePeers("a=10.0.0.1:7000=https://a.example/, b=10.0.0.2:7000")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ok) != 2 {
		t.Fatalf("want 2 peers, got %d", len(ok))
	}
	if ok[0].HTTPURL != "https://a.example" {
		t.Errorf("trailing slash not trimmed: %q", ok[0].HTTPURL)
	}
	if ok[1].HTTPURL != "" {
		t.Errorf("peer b should have no URL, got %q", ok[1].HTTPURL)
	}

	for _, bad := range []string{
		"a",                           // no address
		"a=10.0.0.1",                  // no port
		"a=10.0.0.1:7000=x=y",         // too many fields
		"a=10.0.0.1:7000 a=1.2.3.4:1", // duplicate id: two members voting as one
	} {
		if _, err := ParsePeers(bad); err == nil {
			t.Errorf("ParsePeers(%q) accepted a broken cluster layout", bad)
		}
	}
}

func TestClusterStateDescribesMembershipAndLag(t *testing.T) {
	rs := startCluster(t, 3)
	leader := waitForLeader(t, rs)
	seed(t, leader)
	waitForRoot(t, rs, 2)

	st := leader.node.State()
	if st.Role != "leader" {
		t.Errorf("leader reports role %q", st.Role)
	}
	if len(st.Members) != 3 {
		t.Fatalf("want 3 members, got %d", len(st.Members))
	}
	var leaders, selves int
	for _, m := range st.Members {
		if m.Leader {
			leaders++
		}
		if m.Self {
			selves++
		}
		if !strings.HasPrefix(m.HTTPURL, "http://replica-") {
			t.Errorf("member %s has no public URL: %q", m.ID, m.HTTPURL)
		}
	}
	if leaders != 1 || selves != 1 {
		t.Errorf("membership names %d leaders and %d selves, want 1 and 1", leaders, selves)
	}
	if st.AppliedIndex == 0 {
		t.Error("applied index is 0 after appends")
	}
	// The leader is in contact with itself by definition, so its own
	// last-contact is zero rather than the "never heard from a leader" sentinel.
	if st.LastContactSeconds != 0 {
		t.Errorf("leader reports last_contact_seconds %v, want 0", st.LastContactSeconds)
	}

	// A follower's last contact is the signal lag_entries cannot give. A
	// replica cut off from its leader freezes its commit index alongside its
	// applied one, so it reports zero lag while serving an ever-staler view;
	// only the time since it last heard anything distinguishes that from
	// genuinely being caught up.
	for _, r := range rs {
		st := r.node.State()
		if st.Role == "leader" {
			continue
		}
		if st.LastContactSeconds < 0 {
			t.Errorf("follower %s reports last_contact_seconds %v, but it is in a live cluster",
				st.NodeID, st.LastContactSeconds)
		}
		if st.LastContactSeconds > 10 {
			t.Errorf("follower %s last heard from the leader %vs ago in a healthy cluster",
				st.NodeID, st.LastContactSeconds)
		}
	}
}
