package api

import (
	"strings"
	"testing"
)

// last_contact_seconds carries a sentinel, not just a duration: internal/cluster
// sets -1 when a replica has never heard from a leader, which is the WORST
// contact state. A numeric staleness threshold reads -1 as fresher than fresh
// and renders a partition, or a replica that never joined, in the calm colour
// with the text "last heard from the leader -1s ago".
//
// Asserted on the page source because the branch is the whole point: the values
// are served by /dedi/network and this is the only thing that interprets them.
func TestNeverContactedIsItsOwnState(t *testing.T) {
	page := string(networkPageHTML)
	if !strings.Contains(page, "contact < 0") {
		t.Error("the ring page does not distinguish 'never heard from a leader' from a fresh contact")
	}
	if !strings.Contains(page, "has never heard from a leader") {
		t.Error("the never-contacted case has no wording of its own")
	}
	// It must also be a warning, not merely worded differently.
	if !strings.Contains(page, "outOfTouch || never") {
		t.Error("the never-contacted case does not raise the warning class")
	}
}

// A cluster with no leader is rejecting every write. Without this branch the
// page draws each member as "follower — serves reads" beneath unchanged quorum
// arithmetic, so a set refusing writes looks like a healthy one.
func TestALeaderlessClusterIsFlagged(t *testing.T) {
	page := string(networkPageHTML)
	if !strings.Contains(page, "!c.leader_id") {
		t.Error("the ring page never checks whether the cluster has a leader")
	}
	if !strings.Contains(page, "No leader right now") {
		t.Error("a leaderless cluster produces no warning")
	}
}
