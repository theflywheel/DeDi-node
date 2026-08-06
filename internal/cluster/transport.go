package cluster

import (
	"io"
	"net"
	"time"

	"github.com/hashicorp/raft"
)

// Raft over hostnames rather than resolved addresses.
//
// raft.NewTCPTransport wants the advertised address as a *net.TCPAddr, which
// means resolving this replica's own name at startup. On a platform with an
// internal DNS zone that is a deadlock: Railway registers a service's
// `*.railway.internal` record only once it has a running deployment, so the
// replica cannot start until it is running and cannot run until it starts.
//
// Nothing actually requires the advertised address to be resolved here. It is a
// label other replicas dial, and they resolve it themselves at dial time. So
// this stream layer advertises the hostname verbatim and resolves lazily —
// which also means a replica that comes back on a new address is still
// reachable under the name the cluster agreed on, rather than at whatever IP it
// happened to hold when it first booted.

// hostAddr is a net.Addr that has never been resolved and does not need to be.
type hostAddr string

func (h hostAddr) Network() string { return "tcp" }
func (h hostAddr) String() string  { return string(h) }

type hostnameStreamLayer struct {
	net.Listener
	advertise hostAddr
}

func (s *hostnameStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(address), timeout)
}

func (s *hostnameStreamLayer) Addr() net.Addr { return s.advertise }

// newHostnameTransport listens on bind and tells the rest of the cluster to
// reach this replica at advertise, which may be a name that does not resolve
// yet.
func newHostnameTransport(bind, advertise string, logOutput io.Writer) (*raft.NetworkTransport, error) {
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	return raft.NewNetworkTransport(
		&hostnameStreamLayer{Listener: listener, advertise: hostAddr(advertise)},
		3, 10*time.Second, logOutput,
	), nil
}
