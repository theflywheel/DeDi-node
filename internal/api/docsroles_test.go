package api

import (
	"strings"
	"testing"

	dedi "github.com/theflywheel/DeDi-node"
	"github.com/theflywheel/DeDi-node/internal/provision"
)

// The deploy docs describe the roles an operator can pick. If the renderer
// grows or loses one, the documentation that tells people what to choose from
// stops matching what they can actually get — and a doc shipped inside the
// binary claims the authority of the code it ships with.
func TestDeployDocsCoverEveryRoleTheRendererImplements(t *testing.T) {
	for _, page := range []string{"docs/deployment-modes.md", "docs/railway-template.md"} {
		b, err := dedi.Docs.ReadFile(page)
		if err != nil {
			t.Fatalf("%s: %v", page, err)
		}
		body := string(b)
		for _, role := range provision.RoleNames() {
			if !strings.Contains(body, role) {
				t.Errorf("%s never mentions the %q role, which the console offers", page, role)
			}
		}
	}
}

// "Standalone" meant two different things: mode 1 (nothing independent checks
// this node) and the standalone role (its own key, own log, nothing above it).
// A standalone-role node is a perfectly ordinary thing to run witnessed, so the
// collision made the strongest and weakest configurations share a name.
func TestModeOneIsNotCalledStandalone(t *testing.T) {
	b, err := dedi.Docs.ReadFile("docs/deployment-modes.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if strings.Contains(body, "Mode 1 — Standalone") || strings.Contains(body, "**1 · Standalone**") {
		t.Error("mode 1 is still called Standalone, which now collides with the role of that name")
	}
	if !strings.Contains(body, "Unwitnessed") {
		t.Error("mode 1 no longer says what it is: nothing independent checking the node")
	}
}

// Replicas carry no trust claim. The docs must keep saying so wherever they
// describe the axes, because "three replicas agree" reading as "three parties
// verified" is the single most consequential misreading available here.
func TestDeployDocsKeepReplicationOffTheTrustAxis(t *testing.T) {
	b, _ := dedi.Docs.ReadFile("docs/deployment-modes.md")
	// Whitespace-normalised: prose wraps, and a phrase split across a line
	// break is still the phrase. Matching the raw bytes failed on correct text
	// the first time this ran, which is how a brittle assertion teaches people
	// to reword the doc to suit the test.
	body := strings.Join(strings.Fields(strings.ToLower(string(b))), " ")
	if !strings.Contains(body, "no trust claim") {
		t.Error("the docs no longer state that replication carries no trust claim")
	}
	if !strings.Contains(body, "do not present replicas as witnesses") {
		t.Error("the warning against reading replicas as witnesses is gone")
	}
}
