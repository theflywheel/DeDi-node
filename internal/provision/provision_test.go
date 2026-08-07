package provision

import (
	"strings"
	"testing"
)

func spec() Spec {
	return Spec{
		NodeName:  "beckn-mobility",
		Origin:    "beckn.mobility/log",
		Namespace: "beckn.mobility",
		Image:     "ghcr.io/example/dedi-node:v1",
		ParentURL: "https://parent.example",
		ParentKey: "beckn/log+9ffcb5b4+ARayZZJuokIkD7aEWlM/1xY5Oi/nWg3d75fzMcRplj/N",
		// Deliberately full of characters a shell would act on: real passwords
		// contain them, and this is the value most likely to be pasted into a
		// terminal by an operator who has not read it.
		DatabaseURL: "postgres://u:p'a$(whoami)ss@db:5432/child",
		EnrolToken:  "abc123_token-XYZ",
	}
}

func TestEveryProviderRendersWhatTheChildNeedsToEnrol(t *testing.T) {
	// A rendered artifact missing any one of these produces a node that boots,
	// serves, and never becomes a child — the failure that looks like success.
	for _, name := range Names() {
		p, ok := Get(name)
		if !ok {
			t.Fatalf("Names() returned %q but Get could not resolve it", name)
		}
		art, err := p.Render(spec())
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, want := range []string{
			"beckn.mobility",           // the delegated namespace
			"abc123_token-XYZ",         // the offer token
			"https://parent.example",   // where to enrol
			"DEDI_WILDCARD_NAMESPACES", // the wildcard guard, or the child refuses to boot
		} {
			if !strings.Contains(art.Content, want) {
				t.Errorf("%s: rendered artifact does not mention %q:\n%s", name, want, art.Content)
			}
		}
		if art.Filename == "" || art.Provider != name {
			t.Errorf("%s: artifact is unlabelled: %+v", name, art)
		}
		if len(art.Notes) == 0 {
			t.Errorf("%s: no notes; the token expiry is the thing operators miss", name)
		}
	}
}

func TestShellProvidersQuoteAValueContainingShellMetacharacters(t *testing.T) {
	// The database URL carries a password chosen by someone else. Rendered
	// unquoted into a shell script, $(whoami) is command substitution running
	// on the operator's machine.
	for _, name := range []string{"env", "railway"} {
		p, _ := Get(name)
		art, err := p.Render(spec())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(art.Content, "p'a$(whoami)ss@") {
			t.Errorf("%s: the password is rendered unquoted, so $(whoami) would execute:\n%s", name, art.Content)
		}
		if !strings.Contains(art.Content, `'"'"'`) {
			t.Errorf("%s: expected the embedded single quote to be escaped:\n%s", name, art.Content)
		}
	}
}

func TestAnUnnamedDatabaseLeavesAPlaceholderRatherThanEmpty(t *testing.T) {
	// An empty DEDI_DB_URL starts a node that fails on its first query. A
	// visible placeholder fails at the point the operator is editing config.
	s := spec()
	s.DatabaseURL = ""
	art, err := envProvider{}.Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(art.Content, "postgres://USER:PASSWORD@HOST") {
		t.Errorf("want a placeholder database URL, got:\n%s", art.Content)
	}
}

func TestUnknownProviderIsNotResolved(t *testing.T) {
	if _, ok := Get("terraform"); ok {
		t.Fatal("Get resolved a provider that was never registered")
	}
	// Case and surrounding space come from a form field, not from code.
	if _, ok := Get("  Railway "); !ok {
		t.Fatal("Get should normalise the name it is given")
	}
}
