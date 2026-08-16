package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// specPath is where the vendored DeDi standard lives. Read-only: this suite
// never edits it.
const specPath = "../docs/spec/lfdt/api/openapi.yaml"

// classifiedEndpoints is the suite's map of every endpoint it knows how to
// exercise. TestAllSpecPathsAreClassified fails if openapi.yaml grows an
// endpoint not listed here, so the suite cannot silently go stale.
var classifiedEndpoints = map[string]bool{
	"GET /dedi/lookup/{namespace}":                                 true,
	"GET /dedi/lookup/{namespace}/{registry_name}":                 true,
	"GET /dedi/lookup/{namespace}/{registry_name}/{record_name}":   true,
	"GET /dedi/query/{namespace}":                                  true,
	"GET /dedi/query/{namespace}/{registry_name}":                  true,
	"GET /dedi/versions/{namespace}":                               true,
	"GET /dedi/versions/{namespace}/{registry_name}":               true,
	"GET /dedi/versions/{namespace}/{registry_name}/{record_name}": true,
}

// fixtureValue returns what to substitute for a given path-parameter name,
// using the fixture data startFixtureServer seeds.
func fixtureValue(name string) (string, bool) {
	switch name {
	case "namespace":
		return fixtureNamespace, true
	case "registry_name":
		return fixtureRegistry, true
	case "record_name":
		return fixtureRecord, true
	default:
		return "", false
	}
}

// resolvePath substitutes fixture values for every {param} in the path
// template, failing the test if a param has no known fixture mapping.
func resolvePath(t *testing.T, tmpl string) string {
	t.Helper()
	out := tmpl
	for _, seg := range strings.Split(tmpl, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
			val, ok := fixtureValue(name)
			if !ok {
				t.Fatalf("no fixture value known for path param %q in %q — add one to fixtureValue", name, tmpl)
			}
			out = strings.Replace(out, seg, val, 1)
		}
	}
	return out
}

// validQueryValue returns a value to use for a query parameter, preferring
// its declared enum (so we never trip the 400-on-unknown-enum-value
// behavior) and falling back to a generic placeholder otherwise.
func validQueryValue(p Parameter) string {
	if p.HasEnum() {
		return p.Schema.Enum[0]
	}
	switch p.Schema.Format {
	case "date-time":
		return "2020-01-01T00:00:00Z"
	}
	switch p.Schema.Type {
	case "integer":
		return "1"
	}
	return "x"
}

// getEnvelope performs the GET and decodes the {message, data} envelope,
// failing the test on a transport error or non-2xx status.
func getEnvelope(t *testing.T, srv string, path string, query url.Values, wantStatus int) map[string]any {
	t.Helper()
	u := srv + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET %s: decoding response: %v", u, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s: status %d, want %d — body: %v", u, resp.StatusCode, wantStatus, body)
	}
	return body
}

// assertEnvelope checks the response has exactly the {message, data} shape
// the spec describes for every 200 response in this suite.
func assertEnvelope(t *testing.T, body map[string]any) {
	t.Helper()
	if _, ok := body["message"]; !ok {
		t.Errorf("envelope missing %q key: %v", "message", body)
	}
	if _, ok := body["data"]; !ok {
		t.Errorf("envelope missing %q key: %v", "data", body)
	}
}

// assertRequiredProperties walks schema.Required (and, for object/array
// schemas, recurses into properties/items) confirming every property the
// spec marks required is present in data.
func assertRequiredProperties(t *testing.T, comps Components, schema SchemaObj, data any, path string) {
	t.Helper()
	schema = comps.Resolve(schema)

	switch schema.Type {
	case "array":
		if schema.Items == nil {
			return
		}
		arr, ok := data.([]any)
		if !ok {
			return // nothing to check element shape against
		}
		for i, elem := range arr {
			assertRequiredProperties(t, comps, *schema.Items, elem, fmt.Sprintf("%s[%d]", path, i))
		}
	default:
		obj, ok := data.(map[string]any)
		if !ok {
			if len(schema.Required) > 0 {
				t.Errorf("%s: expected an object with required properties %v, got %T", path, schema.Required, data)
			}
			return
		}
		for _, req := range schema.Required {
			if _, present := obj[req]; !present {
				t.Errorf("%s: required property %q missing from response data: %v", path, req, obj)
			}
		}
		for propName, propSchema := range schema.Properties {
			if child, ok := obj[propName]; ok {
				assertRequiredProperties(t, comps, propSchema, child, path+"."+propName)
			}
		}
	}
}

// TestAllSpecPathsAreClassified fails if the vendored spec has grown a path
// this suite doesn't yet know how to exercise. That is a deliberate,
// loud failure: it forces a maintainer to look at the new endpoint and add
// it to classifiedEndpoints (and to the assertions below) rather than have
// the suite quietly stop covering the standard.
func TestAllSpecPathsAreClassified(t *testing.T) {
	spec, err := LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range spec.Endpoints() {
		if !classifiedEndpoints[ep.Key()] {
			t.Errorf("openapi.yaml declares %s, which this conformance suite does not classify yet.\n"+
				"Add it to classifiedEndpoints in conformance_test.go and give it real coverage — "+
				"do not just add it to make this test pass.", ep.Key())
		}
	}
	// Also catch the suite listing something the spec no longer has, which
	// would mean classifiedEndpoints is stale in the other direction.
	seen := map[string]bool{}
	for _, ep := range spec.Endpoints() {
		seen[ep.Key()] = true
	}
	for key := range classifiedEndpoints {
		if !seen[key] {
			t.Errorf("classifiedEndpoints lists %s, which openapi.yaml no longer declares — remove it", key)
		}
	}
}

// TestSpecPathsResolve asserts, for every path+method the spec declares,
// that substituting fixture values for path parameters yields 200 (not
// 404), the {message, data} envelope, and — where the spec documents a data
// shape — every property it marks required.
func TestSpecPathsResolve(t *testing.T) {
	spec, err := LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := startFixtureServer(t)

	for _, ep := range spec.Endpoints() {
		ep := ep
		t.Run(ep.Key(), func(t *testing.T) {
			if ep.Method != http.MethodGet {
				t.Fatalf("conformance suite only knows how to exercise GET endpoints; %s needs new support", ep.Key())
			}
			path := resolvePath(t, ep.Path)
			body := getEnvelope(t, srv.URL, path, nil, http.StatusOK)
			assertEnvelope(t, body)

			if schema, ok := ep.SuccessDataSchema(spec.Components); ok {
				assertRequiredProperties(t, spec.Components, schema, body["data"], ep.Key()+" data")
			}
		})
	}
}

// TestSpecQueryParametersAccepted asserts, for every documented query
// parameter that declares an enum (status, state, sort, ...), that the
// endpoint accepts it when given one of the spec's own enum values — this
// is the case the DeDi API is strict about: since a status/state value
// outside the documented enum now returns 400, the suite must use a
// spec-valid value or it would be testing the 400 path instead.
//
// Non-enum query parameters (version_id, as_on, name, page, ...) are
// intentionally not exercised here: the spec gives them only a bare type
// (e.g. "string"), not a value the suite can derive as "valid" without
// guessing at implementation-specific semantics. See
// TestVersionIDAcceptsSpecDeclaredType for a documented gap of exactly that
// kind found while building this suite.
func TestSpecQueryParametersAccepted(t *testing.T) {
	spec, err := LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := startFixtureServer(t)

	for _, ep := range spec.Endpoints() {
		ep := ep
		for _, qp := range ep.QueryParams() {
			if !qp.HasEnum() {
				continue
			}
			qp := qp
			t.Run(ep.Key()+" "+qp.Name, func(t *testing.T) {
				path := resolvePath(t, ep.Path)
				q := url.Values{}
				q.Set(qp.Name, validQueryValue(qp))
				body := getEnvelope(t, srv.URL, path, q, http.StatusOK)
				assertEnvelope(t, body)
			})
		}
	}
}

// TestVersionIDTypeMatchesSpec records a real conformance gap this suite
// uncovered while it was being built: openapi.yaml declares the lookup
// endpoints' version_id query parameter as `type: string` with no further
// constraint (no format, no pattern), but the running handler 400s
// ("version_id must be an integer version id") on any value that doesn't
// parse as an integer. A spec-conformant client sending a non-numeric
// string — which the documented type permits — gets rejected.
//
// It was reported rather than asserted while the resolution was open — tighten
// the spec's type, or loosen the handler. Resolved by loosening ours (task
// #54): a value the published contract permits is not a malformed request, so
// an unrecognizable version_id is now "no such version" (404) rather than "your
// request is wrong" (400). Loosening was the safer direction against a spec we
// do not control. Now asserted, so a regression to 400 fails the suite.
func TestVersionIDTypeMatchesSpec(t *testing.T) {
	srv := startFixtureServer(t)
	q := url.Values{}
	q.Set("version_id", "not-an-integer-but-a-valid-spec-string")
	resp, err := http.Get(srv.URL + "/dedi/lookup/" + fixtureNamespace + "?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("openapi.yaml declares version_id as type: string with no format or "+
			"pattern, so a non-numeric value is a well-formed request under the published "+
			"contract; got %d, want 404 (no such version)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unrecognizable version_id names no version: got %d, want 404", resp.StatusCode)
	}
}
