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
func TestTheCatalogueSetsComeFromTheRealRenderer(t *testing.T) {
	for _, info := range RoleCatalogue() {
		if len(info.Sets) == 0 {
			t.Errorf("%s lists no variables at all", info.Role)
			continue
		}
		// Every name must be one envPairs actually emits for that role.
		real := map[string]bool{}
		for _, kv := range envPairs(exampleSpec(info.Role)) {
			real[kv[0]] = true
		}
		for _, name := range info.Sets {
			if !real[name] {
				t.Errorf("%s advertises %s, which its configuration never sets", info.Role, name)
			}
		}
		for name := range real {
			if !contains(info.Sets, name) {
				t.Errorf("%s sets %s but does not say so", info.Role, name)
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
