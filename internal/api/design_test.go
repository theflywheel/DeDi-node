package api

import (
	"strings"
	"testing"
)

// The design is a shared treatment, not a per-page choice.
//
// The pages were built with the structure and the words from the design canvas
// and none of its visual treatment — the nav was plain bold text where the
// design has a tab bar with a rule under it and the current page underlined
// against it. Structure without treatment reads as undesigned, which is what a
// reader actually sees.
func TestEverySurfaceUsesTheSharedNavTreatment(t *testing.T) {
	surfaces := map[string]string{
		"overview.html": string(overviewHTML),
		"index.html":    string(explorerHTML),
		"network.html":  string(networkPageHTML),
		"status.html":   string(statusPageHTML),
		"check.html":    string(checkPageHTML),
		"verify.html":   string(verifyHTML),
		"docs.html":     string(docsHTML),
		"admin.html":    string(adminHTML),
		"generated doc": (&Server{}).docLayout("t", "<p>b</p>"),
	}
	for name, page := range surfaces {
		// The rule under the bar.
		if !strings.Contains(page, "border-bottom: 1px solid #ccc;\n        padding-bottom: .5em") &&
			!strings.Contains(page, "border-bottom: 1px solid #ccc; padding-bottom: .5em") {
			t.Errorf("%s: the nav has no rule under it", name)
		}
		// And the current page underlined against it, on the surfaces that mark one.
		if strings.Contains(page, "nav a.sel") &&
			!strings.Contains(page, "border-bottom: 2px solid #1a1a1a") {
			t.Errorf("%s: the current page is not underlined in the nav", name)
		}
		if !strings.Contains(page, "flex-wrap: wrap") {
			t.Errorf("%s: the nav cannot wrap", name)
		}
	}
}
