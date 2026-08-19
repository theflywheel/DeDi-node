package api

import (
	"regexp"
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
	// Extract the nav rule and check its declarations, rather than matching a
	// formatted string. The first version needed two spellings of the same rule
	// to cope with line wrapping, which is a sign the assertion was about
	// whitespace rather than about the design.
	navRule := regexp.MustCompile(`(?s)\bnav \{(.*?)\}`)
	selRule := regexp.MustCompile(`(?s)nav a\.sel \{(.*?)\}`)
	decls := func(rule *regexp.Regexp, page string) string {
		m := rule.FindStringSubmatch(page)
		if m == nil {
			return ""
		}
		return strings.Join(strings.Fields(m[1]), " ")
	}

	for name, page := range surfaces {
		nav := decls(navRule, page)
		if nav == "" {
			t.Errorf("%s: no nav rule at all", name)
			continue
		}
		for _, want := range []string{"flex-wrap: wrap", "border-bottom: 1px solid #ccc"} {
			if !strings.Contains(nav, want) {
				t.Errorf("%s: the nav rule lacks %q — it is %q", name, want, nav)
			}
		}
		if sel := decls(selRule, page); sel != "" {
			if !strings.Contains(sel, "border-bottom: 2px solid #1a1a1a") {
				t.Errorf("%s: the current page is not underlined in the nav — %q", name, sel)
			}
		}
	}
}
