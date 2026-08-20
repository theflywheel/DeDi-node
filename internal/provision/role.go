package provision

import (
	"fmt"
	"sort"
	"strings"

	"github.com/theflywheel/DeDi-node/internal/cluster"
)

// A node's role is what it is to the other nodes — and until now it existed
// only as a combination of environment variables an operator had to know.
//
// The five below are all already implemented by cmd/dedid; nothing here adds a
// capability. What it adds is a name for each, so that creating a node is a
// choice between five understandable things rather than a correct guess about
// which of forty DEDI_* variables matter and which must be left unset.
//
// They are not mutually exclusive, and a picker that pretended otherwise would
// be lying about how the node works: a child is normally also witnessed by its
// parent, and a standalone registry can be replicated and witness someone at
// the same time. So Role is the primary shape and WitnessTarget composes with
// any of them.
type Role string

// Delegated reports whether this role is granted its namespace by another
// node. Only a child is: the rest are nodes an operator stands up, and minting
// a delegation offer for one writes a record into the parent's public log
// claiming a delegation that nothing can ever redeem.
func (r Role) Delegated() bool { return r == RoleChild || r == "" }

const (
	// RoleStandalone is a directory of its own: its own namespace, its own
	// signing key, its own log. Nothing above it, nothing depending on it.
	RoleStandalone Role = "standalone"

	// RoleMirror serves reads and nothing else. With no publisher keys the
	// write plane is not merely refused, it is never routed — /admin answers
	// 404 rather than 401, because a read-only node should not advertise a
	// door it does not have.
	RoleMirror Role = "mirror"

	// RoleWitness fetches another node's signed checkpoints and consistency
	// proofs on an interval and records each verdict in its own log. The
	// target cannot rewrite history without this node holding a proof of it.
	RoleWitness Role = "witness"

	// RoleReplica is one member of a replica set: same identity, same key,
	// same log, kept in step by Raft. Uptime, not trust — three replicas
	// agreeing is one party speaking three times, and must never be read as
	// three parties verifying.
	//
	// Membership is fixed at bootstrap. internal/cluster calls
	// BootstrapCluster and has no AddVoter, so there is no way to add a
	// replica to a running cluster: the whole set is configured together, with
	// DEDI_CLUSTER_BOOTSTRAP on exactly one member on its first start.
	RoleReplica Role = "replica"

	// RoleChild is a separate node holding one namespace another node
	// delegates to it: its own key, its own database, its own log, run by
	// someone else and governed by the delegation.
	RoleChild Role = "child"
)

// roleInfo is what the console shows beside each choice. It lives here rather
// than in the page so the two cannot drift: the same words describe the role
// that renders the config.
type roleInfo struct {
	Role     Role   `json:"role"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Identity string `json:"identity"`
	Log      string `json:"log"`
	Buys     string `json:"buys"`
	Writes   string `json:"writes"`
	// Needs names the inputs the operator must supply for this role. The
	// console uses it to show only the fields that matter.
	Needs []string `json:"needs"`

	// Sets names the environment variables this role's configuration will
	// carry. Filled by RoleCatalogue from the real renderer rather than
	// written out here, so the console cannot show one list while the node is
	// configured by another.
	Sets []string `json:"sets"`
}

// setsFor asks the renderer which variables a role actually produces.
//
// The console shows this when an operator picks a role, before any node is
// created, so the answer has to be true without a complete spec. It is derived
// by rendering a fully-populated example of that role and taking the KEYS —
// never the values, which are the example's and mean nothing — so a change to
// envPairs shows up here on the next build instead of leaving a hand-written
// list quietly describing the configuration of an older version.
func setsFor(r Role) []string {
	example := exampleSpec(r)
	var out []string
	for _, kv := range envPairs(example) {
		out = append(out, kv[0])
	}
	sort.Strings(out)
	return out
}

// exampleSpec is a spec of one role, carrying exactly the inputs that role
// declares it needs, used only to ask the renderer which variables the role
// produces. Its values are never shown to anyone.
//
// Filling in everything for every role made two roles indistinguishable, which
// the catalogue test caught: witnessing composes with ANY role, so a standalone
// handed a witness target sets precisely the variables a witness does. What
// separates the five is the configuration each one REQUIRES, so that is what
// the example carries — and the witness-target pair stays where it belongs, on
// the role that cannot work without it.
func exampleSpec(r Role) Spec {
	needs := map[string]bool{}
	for _, n := range roles[r].Needs {
		needs[n] = true
	}
	s := Spec{
		Role: r, NodeName: "example", Origin: "example.org/log",
		PublicURL: "https://example.org", DatabaseURL: "postgres://example",
	}
	if needs["namespace"] || r == RoleChild {
		s.Namespace = "example"
	}
	if needs["enrolment"] {
		s.ParentURL, s.ParentKey, s.EnrolToken = "https://parent.example", "parent+key", "token"
	}
	if needs["witness_target"] {
		s.WitnessTargetURL = "https://target.example"
		s.WitnessTargetKey = "target+key"
		s.WitnessTargetOrigin = "target.example/log"
	}
	if needs["cluster"] {
		s.ClusterID, s.ClusterPeers = "set-1", "a=10.0.0.1:7000,b=10.0.0.2:7000"
		s.ClusterBind, s.ClusterDataDir = "0.0.0.0:7000", "/data/raft"
	}
	if needs["identity"] {
		s.SharedKeyFile = "/keys/set.key"
	}
	if needs["crawl"] {
		s.CrawlDomains = "example.org"
	}
	return s
}

var roles = map[Role]roleInfo{
	RoleStandalone: {
		Role: RoleStandalone, Title: "Standalone registry",
		Summary:  "A directory of its own. This is what a first node is.",
		Identity: "its own key", Log: "its own", Buys: "a directory you govern",
		Writes: "open once you add a publisher key",
		Needs:  []string{"namespace", "database"},
	},
	RoleMirror: {
		Role: RoleMirror, Title: "Read-only mirror",
		Summary:  "Serves reads and nothing else; its write plane is never routed.",
		Identity: "its own key", Log: "its own, fed by crawling",
		Buys:   "reach, and a cache that can still be checked",
		Writes: "none — /admin answers 404, not 401",
		Needs:  []string{"database", "crawl"},
	},
	RoleWitness: {
		Role: RoleWitness, Title: "Witness",
		Summary:  "Proves another node's log is append-only, and records each verdict.",
		Identity: "its own key", Log: "its own, holding verdicts",
		Buys: "tamper evidence for a log you do not run",
		// The HTTP write plane is CLOSED on a witness: it holds no publisher
		// keys. Its verdicts reach the log by a different road entirely — the
		// witness loop appends to the store directly — so saying "open" here
		// described a door that is not there and invited an operator to open
		// one the node then refuses to start without more configuration.
		Writes: "none — verdicts bypass the write plane entirely",
		Needs:  []string{"database", "witness_target"},
	},
	RoleReplica: {
		Role: RoleReplica, Title: "Replica",
		Summary:  "One member of a replica set, configured together at bootstrap. Uptime, not trust.",
		Identity: "the set's key, shared — you must supply it", Log: "one log, replicated",
		Buys:   "survival of one machine failing",
		Writes: "the leader accepts; followers redirect",
		// identity and origin are not optional: the daemon refuses to start
		// with DEDI_CLUSTER_ID and no shared key, and a replica signing under
		// its own origin would break the cluster's one-origin invariant the
		// moment leadership moved to it.
		Needs: []string{"database", "cluster", "identity", "origin"},
	},
	RoleChild: {
		Role: RoleChild, Title: "Child (delegated)",
		Summary:  "Holds one namespace another node delegates to it.",
		Identity: "its own key, generated on first boot", Log: "its own, delegated",
		Buys:   "a namespace someone else runs",
		Writes: "open, scoped to the delegated namespace",
		Needs:  []string{"database", "enrolment"},
	},
}

// ParseRole resolves a role name, defaulting to child.
//
// Child is the default because delegation is the only path that existed before
// roles were named, and every caller of it predates this: an empty role has
// always meant "the node a parent is provisioning".
func ParseRole(s string) (Role, error) {
	t := Role(strings.TrimSpace(strings.ToLower(s)))
	if t == "" {
		return RoleChild, nil
	}
	if _, ok := roles[t]; !ok {
		return "", fmt.Errorf("unknown role %q; available: %s", s, strings.Join(RoleNames(), ", "))
	}
	return t, nil
}

// RoleNames lists the roles, sorted, for the console to offer.
func RoleNames() []string {
	out := make([]string, 0, len(roles))
	for r := range roles {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}

// RoleCatalogue describes every role, for the console to render.
func RoleCatalogue() []roleInfo {
	out := make([]roleInfo, 0, len(roles))
	for _, name := range RoleNames() {
		info := roles[Role(name)]
		info.Sets = setsFor(Role(name))
		out = append(out, info)
	}
	return out
}

// Validate reports what a Spec is missing for its role.
//
// This exists because every missing input fails LATER and QUIETLY. A replica
// with a blank cluster id does not error: openCluster sees an empty id, returns
// nil, and the node comes up healthy as an ordinary unclustered node that
// generates an identity of its own — which is precisely the "second node
// claiming the first one's name" the replica note warns about, with nothing
// anywhere reporting it. A mirror with no domains crawls nothing. A witness
// with no target witnesses nobody and looks fine doing it.
//
// Rendering a config that cannot do the job it was asked for is worse than
// refusing, because the operator finds out from behaviour rather than from an
// error.
func (s Spec) Validate() error {
	r := s.Role
	if r == "" {
		r = RoleChild
	}
	var missing []string
	need := func(cond bool, what string) {
		if !cond {
			missing = append(missing, what)
		}
	}
	switch r {
	case RoleChild:
		need(strings.TrimSpace(s.Namespace) != "", "namespace")
		need(strings.TrimSpace(s.EnrolToken) != "", "enrolment token")
		need(strings.TrimSpace(s.ParentURL) != "", "parent URL")
	case RoleReplica:
		need(strings.TrimSpace(s.ClusterID) != "", "cluster id")
		need(strings.TrimSpace(s.ClusterPeers) != "", "cluster peers")
		// Parsed with the daemon's own parser, not merely checked for
		// non-emptiness. cmd/dedid.openCluster calls cluster.ParsePeers and
		// cluster.Open then refuses an id absent from the parsed list, so
		// "not-a-peer", or a member id naming nobody in its own set, rendered
		// a perfectly plausible artifact that died on first boot. A second
		// opinion about the format would be a second thing to keep in step;
		// this asks the code that will actually read it.
		if spec := strings.TrimSpace(s.ClusterPeers); spec != "" {
			peers, err := cluster.ParsePeers(spec)
			if err != nil {
				missing = append(missing, "a parseable peer list ("+err.Error()+")")
			} else if id := strings.TrimSpace(s.ClusterID); id != "" {
				found := false
				for _, p := range peers {
					if p.ID == id {
						found = true
						break
					}
				}
				if !found {
					missing = append(missing, "its own id "+id+" in the peer list — a member "+
						"absent from its own set is refused at boot")
				}
			}
		}
		// The two that make a replica a replica rather than a new node.
		need(strings.TrimSpace(s.SharedKeyFile) != "", "the set's shared key file")
		need(strings.TrimSpace(s.Origin) != "", "the set's origin")
	case RoleMirror:
		need(strings.TrimSpace(s.CrawlDomains) != "", "domains to mirror")
	case RoleWitness:
		need(strings.TrimSpace(s.WitnessTargetURL) != "", "witness target URL")
		need(strings.TrimSpace(s.WitnessTargetKey) != "", "witness target verifier key")
	case RoleStandalone:
		need(strings.TrimSpace(s.Namespace) != "", "namespace")
	}
	// Role-independent, because witnessing composes with every role. The
	// daemon starts the witness loop whenever the URL is set, and then
	// note.NewVerifier("") fails on every single run — so the node comes up
	// healthy, reports a witness loop, and silently never verifies anything.
	// Half a witness tuple is worse than none.
	if strings.TrimSpace(s.WitnessTargetURL) != "" && strings.TrimSpace(s.WitnessTargetKey) == "" {
		missing = append(missing, "the witness target's verifier key (a target URL without it starts "+
			"a loop that fails every run)")
	}

	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("a %s needs %s", r, strings.Join(missing, ", "))
}
