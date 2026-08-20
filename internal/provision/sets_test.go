package provision

import (
	"strings"
	"testing"
)

// The console shows an operator which variables a role's configuration will
// carry, before any node exists. That list has to be the renderer's own answer.
//
// The design canvas drew this block with the variables typed out by hand, and
// its example was already wrong — it showed a cluster peers value in a format
// the daemon rejects (see TestPeerPlaceholderParses). A hand-written list is a
// second description of the configuration, and the second description is the
// one that goes stale.
// What an operator must see for each role, written down independently.
//
// The first version of this test asked whether setsFor's output equalled
// envPairs(exampleSpec(role)) — which is setsFor's own definition, so it could
// not fail for any exampleSpec however wrong, and the whole stated purpose
// (the console cannot describe one configuration while the node writes
// another) went unverified. A test whose expectation is the implementation
// restated is a tautology wearing the clothes of a check.
//
// So the expectation here is a hand-written table of what each role's
// configuration MUST name, derived from what the role is rather than from the
// code that renders it. It will need updating when a role genuinely changes
// what it needs, which is the point: that is a decision someone should have to
// make on purpose.
func TestEachRoleAdvertisesTheConfigurationItsJobRequires(t *testing.T) {
	// Every node, whatever it is, needs to know its name, its origin, where to
	// listen and where its database is.
	common := []string{"DEDI_NODE_NAME", "DEDI_ORIGIN", "DEDI_LISTEN", "DEDI_DB_URL"}
	want := map[Role][]string{
		// A witness is defined by having a target; without one it witnesses
		// nobody and looks healthy doing it.
		// URL and key, not origin: the origin is optional in the renderer and
		// the console has no field for it, so a role that advertised it would
		// be naming a variable this page can never produce.
		RoleWitness: {"DEDI_WITNESS_TARGET_URL", "DEDI_WITNESS_TARGET_KEY"},
		// A replica that does not know its set is not a replica; the daemon
		// refuses to start without the shared identity.
		RoleReplica: {"DEDI_CLUSTER_ID", "DEDI_CLUSTER_PEERS", "DEDI_CLUSTER_BIND",
			"DEDI_CLUSTER_DATA_DIR", "DEDI_KEY_FILE"},
		// A mirror with no domains crawls nothing.
		RoleMirror: {"DEDI_CRAWL_DOMAINS"},
		// A child is granted its namespace, and must be able to reach and
		// verify the parent granting it.
		RoleChild: {"DEDI_ENROL_TOKEN", "DEDI_ENROL_NAMESPACE", "DEDI_PARENT_URL", "DEDI_PARENT_KEY"},
		// A standalone needs nothing beyond the common four.
		RoleStandalone: {},
	}
	// And what each role must NOT advertise, because it will never be given it.
	notWant := map[Role][]string{
		RoleStandalone: {"DEDI_CLUSTER_ID", "DEDI_CRAWL_DOMAINS", "DEDI_ENROL_TOKEN"},
		RoleMirror:     {"DEDI_CLUSTER_ID", "DEDI_ENROL_TOKEN"},
		RoleWitness:    {"DEDI_CLUSTER_ID", "DEDI_CRAWL_DOMAINS", "DEDI_WITNESS_TARGET_ORIGIN"},
		RoleReplica:    {"DEDI_CRAWL_DOMAINS", "DEDI_ENROL_TOKEN"},
		RoleChild:      {"DEDI_CLUSTER_ID", "DEDI_CRAWL_DOMAINS"},
	}

	byRole := map[Role][]string{}
	for _, info := range RoleCatalogue() {
		byRole[info.Role] = info.Sets
	}
	if len(byRole) != len(want) {
		t.Fatalf("the catalogue offers %d roles but this test knows %d; one of them is new",
			len(byRole), len(want))
	}
	for r, must := range want {
		got, ok := byRole[r]
		if !ok {
			t.Errorf("%s is not in the catalogue", r)
			continue
		}
		for _, name := range append(append([]string{}, common...), must...) {
			if !contains(got, name) {
				t.Errorf("%s does not advertise %s, which its configuration needs", r, name)
			}
		}
		for _, name := range notWant[r] {
			if contains(got, name) {
				t.Errorf("%s advertises %s, which it will never be given", r, name)
			}
		}
	}
}

// And the roles must still differ. A list identical across all five would
// satisfy the test above while telling an operator nothing.
func TestRolesAdvertiseDifferentConfiguration(t *testing.T) {
	seen := map[string]string{}
	for _, info := range RoleCatalogue() {
		key := strings.Join(info.Sets, ",")
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s advertise identical configuration, so the picker distinguishes nothing",
				other, info.Role)
		}
		seen[key] = string(info.Role)
	}
	// The specific distinctions that matter most: only a replica is configured
	// with a cluster, only a mirror crawls, and a witness is told who to watch.
	find := func(r Role) []string {
		for _, i := range RoleCatalogue() {
			if i.Role == r {
				return i.Sets
			}
		}
		t.Fatalf("no %s in the catalogue", r)
		return nil
	}
	if !contains(find(RoleReplica), "DEDI_CLUSTER_ID") {
		t.Error("a replica does not advertise its cluster")
	}
	if contains(find(RoleStandalone), "DEDI_CLUSTER_ID") {
		t.Error("a standalone advertises cluster configuration it will never be given")
	}
	if !contains(find(RoleMirror), "DEDI_CRAWL_DOMAINS") {
		t.Error("a mirror does not advertise what it crawls")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
