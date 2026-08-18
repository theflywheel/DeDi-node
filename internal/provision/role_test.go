package provision

import (
	"strings"
	"testing"
)

func envOf(t *testing.T, s Spec) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range envPairs(s) {
		out[kv[0]] = kv[1]
	}
	return out
}

// The whole point of naming roles is that each one renders the configuration
// that role actually needs — and, just as importantly, does NOT render the
// configuration it must not have. A replica that enrols is not a replica; a
// mirror with a publisher key is not read-only.
func TestEachRoleRendersOnlyItsOwnConfiguration(t *testing.T) {
	base := Spec{NodeName: "n", Origin: "n.example/log", Namespace: "ns",
		ParentURL: "https://parent.example", ParentKey: "parent+key", EnrolToken: "tok",
		ClusterID: "ha-2", ClusterPeers: "ha-1@a:7000", CrawlDomains: "a.example"}

	cases := []struct {
		role    Role
		want    []string
		wantNot []string
	}{
		{RoleChild,
			[]string{"DEDI_PARENT_URL", "DEDI_ENROL_TOKEN", "DEDI_ENROL_NAMESPACE"},
			[]string{"DEDI_CLUSTER_ID", "DEDI_CRAWL_DOMAINS", "DEDI_WITNESS_TARGET_URL"}},
		{RoleReplica,
			[]string{"DEDI_CLUSTER_ID", "DEDI_CLUSTER_PEERS", "DEDI_CLUSTER_BIND"},
			[]string{"DEDI_ENROL_TOKEN", "DEDI_PARENT_URL", "DEDI_CRAWL_DOMAINS"}},
		{RoleMirror,
			[]string{"DEDI_CRAWL_DOMAINS"},
			[]string{"DEDI_ENROL_TOKEN", "DEDI_CLUSTER_ID", "DEDI_PUBLISHER_KEYS"}},
		{RoleStandalone,
			[]string{"DEDI_DB_URL", "DEDI_ORIGIN"},
			[]string{"DEDI_ENROL_TOKEN", "DEDI_CLUSTER_ID", "DEDI_CRAWL_DOMAINS"}},
		{RoleWitness,
			[]string{"DEDI_DB_URL"},
			[]string{"DEDI_ENROL_TOKEN", "DEDI_CLUSTER_ID"}},
	}
	for _, c := range cases {
		s := base
		s.Role = c.role
		env := envOf(t, s)
		for _, k := range c.want {
			if _, ok := env[k]; !ok {
				t.Errorf("%s: %s missing", c.role, k)
			}
		}
		for _, k := range c.wantNot {
			if _, ok := env[k]; ok {
				t.Errorf("%s: %s present, but a %s must not have it", c.role, k, c.role)
			}
		}
	}
}

// Roles compose. A picker that forced one would be lying about the node: a
// child is normally witnessed by its parent, and a standalone registry can
// witness a peer while being replicated.
func TestWitnessingComposesWithEveryRole(t *testing.T) {
	for _, r := range []Role{RoleStandalone, RoleMirror, RoleReplica, RoleChild, RoleWitness} {
		s := Spec{NodeName: "n", Origin: "o", Role: r,
			WitnessTargetURL: "https://b.example/dedi", WitnessTargetKey: "b+key"}
		env := envOf(t, s)
		if env["DEDI_WITNESS_TARGET_URL"] != "https://b.example/dedi" {
			t.Errorf("%s cannot also witness, but nothing about the role prevents it", r)
		}
	}
}

// A replica that generates its own key is not a replica — it is a second node
// claiming the first one's name — and that is the one mistake here whose
// symptom appears far from its cause.
func TestReplicaIsWarnedAboutIdentity(t *testing.T) {
	notes := strings.ToLower(strings.Join(commonNotes(Spec{Role: RoleReplica, NodeName: "n"}), " "))
	for _, must := range []string{
		"same key file",    // or it is a second node claiming the first one's name
		"same dedi_origin", // or the set breaks its single-origin invariant
		"uptime, not trust",
		"bootstrap", // membership is fixed there; there is no join path
	} {
		if !strings.Contains(notes, must) {
			t.Errorf("a replica is not told about %q", must)
		}
	}
}

// The daemon refuses to start with DEDI_CLUSTER_ID and no shared identity, and
// a replica with no cluster id comes up silently as an ordinary node that
// generates an identity of its own — the worst outcome available, because
// nothing reports it. Neither config should be renderable.
func TestReplicaWithoutIdentityOrOriginIsRefused(t *testing.T) {
	full := Spec{Role: RoleReplica, NodeName: "n", Origin: "set.example/log",
		ClusterID: "ha-2", ClusterPeers: "ha-1@a:7000", SharedKeyFile: "/keys/cluster.key"}
	if err := full.Validate(); err != nil {
		t.Fatalf("a complete replica spec was refused: %v", err)
	}
	for _, drop := range []func(*Spec){
		func(s *Spec) { s.SharedKeyFile = "" },
		func(s *Spec) { s.Origin = "" },
		func(s *Spec) { s.ClusterID = "" },
		func(s *Spec) { s.ClusterPeers = "" },
	} {
		s := full
		drop(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("an incomplete replica spec was accepted: %+v", s)
		}
	}
}

// Every role refuses what it cannot do the job without, because each of these
// omissions fails later and quietly rather than at render time.
func TestEachRoleRefusesAConfigThatCannotDoItsJob(t *testing.T) {
	for _, c := range []struct {
		role Role
		bad  Spec
	}{
		{RoleMirror, Spec{Role: RoleMirror}},
		{RoleWitness, Spec{Role: RoleWitness}},
		{RoleStandalone, Spec{Role: RoleStandalone}},
		{RoleChild, Spec{Role: RoleChild}},
	} {
		if err := c.bad.Validate(); err == nil {
			t.Errorf("%s: a config with none of its inputs was accepted", c.role)
		}
	}
}

// Only a child is delegated a namespace. The other four are nodes an operator
// stands up, and minting a delegation for one writes a claim into the parent's
// public log that nothing can ever redeem.
func TestOnlyAChildIsDelegated(t *testing.T) {
	if !RoleChild.Delegated() || !Role("").Delegated() {
		t.Error("a child is not treated as delegated")
	}
	for _, r := range []Role{RoleStandalone, RoleMirror, RoleWitness, RoleReplica} {
		if r.Delegated() {
			t.Errorf("%s is treated as delegated, so creating one would mint an unredeemable offer", r)
		}
	}
}

// An empty role means child, because delegation is the only path that existed
// before roles were named and every caller of it predates this.
func TestEmptyRoleIsChildForCompatibility(t *testing.T) {
	got, err := ParseRole("")
	if err != nil || got != RoleChild {
		t.Fatalf("ParseRole(\"\") = %q, %v; want child", got, err)
	}
	if _, err := ParseRole("nonsense"); err == nil {
		t.Error("an unknown role was accepted")
	}
}

// The console renders from this catalogue, so it cannot describe a role the
// renderer does not implement.
func TestCatalogueCoversEveryRole(t *testing.T) {
	cat := RoleCatalogue()
	if len(cat) != len(roles) {
		t.Fatalf("catalogue has %d entries for %d roles", len(cat), len(roles))
	}
	for _, r := range cat {
		if r.Title == "" || r.Summary == "" || len(r.Needs) == 0 {
			t.Errorf("%s is offered with nothing to explain it: %+v", r.Role, r)
		}
	}
}
