package refschemas

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNamesMatchesSpecFilenames(t *testing.T) {
	want := map[string]bool{
		"Beckn_subscriber":           true,
		"Beckn_subscriber_reference": true,
		"public_key":                 true,
		"membership":                 true,
		"revoke":                     true,
	}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %d entries", got, len(want))
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("Names() contains unexpected entry %q", n)
		}
	}
}

// All five embedded files must be valid JSON and look like a JSON Schema
// draft: an object with a "type" (or at least "properties"/"required")
// keyword, not just arbitrary JSON.
func TestAllSchemasParseAsJSONSchema(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			raw, ok := Lookup(name)
			if !ok {
				t.Fatalf("Lookup(%q) not found", name)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("schema %q is not valid JSON: %v", name, err)
			}
			if _, hasType := m["type"]; !hasType {
				if _, hasProps := m["properties"]; !hasProps {
					t.Fatalf("schema %q has neither \"type\" nor \"properties\" — does not look like a JSON Schema", name)
				}
			}
			if _, ok := m["$schema"]; !ok {
				t.Errorf("schema %q has no \"$schema\" draft declaration", name)
			}

			obj, found, err := LookupObject(name)
			if err != nil {
				t.Fatalf("LookupObject(%q): %v", name, err)
			}
			if !found {
				t.Fatalf("LookupObject(%q) not found", name)
			}
			if obj["type"] != m["type"] {
				t.Errorf("LookupObject(%q) type = %v, want %v", name, obj["type"], m["type"])
			}
		})
	}
}

func TestLookupUnknownName(t *testing.T) {
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("Lookup(\"does-not-exist\") = ok, want not found")
	}
	if _, ok := Lookup("../schemas/public_key"); ok {
		t.Fatal("Lookup with path traversal attempt should not be found")
	}
}

// Spec §10 declares the canonical schema URLs but points them at the mutable
// `main` branch, and says in the same breath that they MUST be pinned to an
// immutable ref: editing a schema on main would silently change the meaning of
// every DeDi file referencing it, including ones we have already signed.
func TestURLIsPinnedToAnImmutableRef(t *testing.T) {
	for _, name := range Names() {
		u := URL(name)
		if strings.Contains(u, "/main/") {
			t.Errorf("%s: URL points at the mutable main branch: %s", name, u)
		}
		if !strings.Contains(u, SpecCommit) {
			t.Errorf("%s: URL is not pinned to SpecCommit: %s", name, u)
		}
		if !strings.HasSuffix(u, "/schemas/"+name+".json") {
			t.Errorf("%s: unexpected URL shape: %s", name, u)
		}
	}
}

// The URL we advertise and the bytes we embed must name the same commit,
// otherwise a consumer fetching the pinned URL gets a schema we do not enforce.
func TestSpecCommitMatchesTheVendoredCopy(t *testing.T) {
	readme, err := os.ReadFile("../../docs/spec/README.md")
	if err != nil {
		t.Skipf("vendored spec README not readable: %v", err)
	}
	if !strings.Contains(string(readme), SpecCommit) {
		t.Errorf("SpecCommit %s is not the commit docs/spec/README.md records as vendored", SpecCommit)
	}
}
