package cluster

import (
	"io"
	"net"
	"strings"
	"testing"
)

// Raft must advertise this replica by the name it was configured with, not by
// a resolved address.
//
// The reason is not cosmetic. On Railway a service's `*.railway.internal`
// record exists only once it has a running deployment, so resolving your own
// name at startup deadlocks: you cannot start until you are running and cannot
// run until you start. The first deployment of this cluster failed exactly
// that way, in a loop, with "no such host". Resolving eagerly is also wrong
// over time — a replica that comes back on a different address should still be
// reachable under the name the cluster agreed on.
func TestTransportAdvertisesTheConfiguredNameUnresolved(t *testing.T) {
	// A name that does not resolve anywhere. If anything in the path tries to
	// resolve the advertised address, this fails to construct.
	const advertise = "replica-7.railway.internal:7000"

	transport, err := newHostnameTransport("127.0.0.1:0", advertise, io.Discard)
	if err != nil {
		t.Fatalf("transport with an unresolvable advertised name: %v", err)
	}
	defer transport.Close()

	if got := string(transport.LocalAddr()); got != advertise {
		t.Fatalf("advertised %q, want %q — peers would try to reach this replica at the wrong name",
			got, advertise)
	}
}

func TestTransportStillFailsOnAnUnusableBindAddress(t *testing.T) {
	// Lazy resolution must not turn a genuinely broken listen address into a
	// silent success, or a replica would look healthy while listening nowhere.
	if _, err := newHostnameTransport("256.256.256.256:7000", "x:7000", io.Discard); err == nil {
		t.Fatal("bound to an impossible address")
	}
}

func TestClusterFormsWhenPeersAreNamedByHostname(t *testing.T) {
	// The Railway configuration addresses peers by name. Locally "localhost"
	// is the only name guaranteed to resolve, but it exercises the same path:
	// the transport advertises a name and peers resolve it when they dial.
	rs := startClusterAt(t, 3, func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve port: %v", err)
		}
		defer l.Close()
		_, port, _ := net.SplitHostPort(l.Addr().String())
		return "localhost:" + port
	})
	leader := waitForLeader(t, rs)
	if !strings.HasPrefix(leader.node.cfg.Peers[0].RaftAddr, "localhost:") {
		t.Fatal("test did not actually use hostnames")
	}
	seed(t, leader)
	waitForRoot(t, rs, 2)
}
