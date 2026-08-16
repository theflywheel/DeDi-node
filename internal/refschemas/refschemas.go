// Package refschemas embeds the reference registry schemas the DeDi standard
// ships (docs/spec/lfdt/schemas/) so a node can offer them as built-ins.
//
// Operators today paste a registry's schema by hand into the `schema` field
// of a putRegistry payload, which means two nodes can both claim a
// "public_key" registry while enforcing different shapes for it — one may
// require keyFormat, another may not. Letting a registry instead reference a
// built-in by name (see internal/api/admin.go's resolveBuiltinSchema) makes
// the five reference registries consistent across nodes, because the schema
// text ships with the binary rather than being retyped per deployment.
//
// The schema files under ./schemas are a copy of docs/spec/lfdt/schemas, not
// a symlink or vendored reference to it: go:embed cannot reach outside its
// package directory, so `go generate` (see generate.go) copies the five
// registry schemas in on demand. docs/spec/lfdt/schemas is otherwise the
// authoritative, read-only vendored copy of the LFDT spec — do not edit it,
// and do not edit ./schemas by hand either; regenerate instead.
package refschemas

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed schemas/*.json
var schemaFS embed.FS

// SpecCommit is the LFDT spec commit ./schemas was copied from. Keep it in
// step with generate.go and docs/spec/README.md when refreshing.
const SpecCommit = "52e120d53b1b2df94aca9cf18c8a1aa47e8a4f18"

// URL returns the canonical URL for a reference schema, pinned to SpecCommit.
//
// The pin is required, not cosmetic. Spec §10 declares the canonical schema
// URLs but points them at the mutable `main` branch, and flags this in the text
// itself: "editing a schema on main would silently change the meaning of every
// DeDi file that references it ... these MUST be pinned to an immutable ref
// before release." A published file we sign today asserts a shape; if the URL
// it names can be rewritten upstream tomorrow, our signature covers a reference
// whose meaning has moved out from under it.
//
// A commit SHA is the immutable ref available now — upstream has cut no version
// tag — and it is the same commit these files were copied from, so what we
// advertise and what we enforce cannot drift apart.
func URL(name string) string {
	return "https://raw.githubusercontent.com/LF-Decentralized-Trust-labs/" +
		"decentralized-directory-protocol/" + SpecCommit + "/schemas/" + name + ".json"
}

// names lists the embedded schemas, spec filename minus ".json". Kept
// explicit (rather than derived from a directory read) so Names() has a
// stable, predictable order without a sort at call time mattering for
// anything but presentation.
var names = []string{
	"Beckn_subscriber",
	"Beckn_subscriber_reference",
	"public_key",
	"membership",
	"revoke",
}

// Names returns the built-in schema names, sorted.
func Names() []string {
	out := make([]string, len(names))
	copy(out, names)
	sort.Strings(out)
	return out
}

// Lookup returns the raw JSON of the built-in schema registered under name,
// and whether one exists.
func Lookup(name string) (json.RawMessage, bool) {
	known := false
	for _, n := range names {
		if n == name {
			known = true
			break
		}
	}
	if !known {
		return nil, false
	}
	raw, err := schemaFS.ReadFile("schemas/" + name + ".json")
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

// LookupObject returns the built-in schema decoded into a map, as
// ValidateAgainstSchema and the registry payload's `schema` field expect it.
func LookupObject(name string) (map[string]any, bool, error) {
	raw, ok := Lookup(name)
	if !ok {
		return nil, false, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, true, fmt.Errorf("refschemas: built-in schema %q does not parse as JSON: %w", name, err)
	}
	return m, true, nil
}
