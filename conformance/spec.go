package conformance

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the subset of an OpenAPI 3 document this package needs: paths,
// their operations, and the component schemas those operations reference.
// It is parsed straight from YAML, so a change to openapi.yaml is picked up
// the next time the suite runs — nothing here is hand-copied.
type Spec struct {
	Paths      map[string]PathItem `yaml:"paths"`
	Components Components          `yaml:"components"`
}

// PathItem maps an HTTP method (lowercase, e.g. "get") to its Operation.
type PathItem map[string]Operation

type Operation struct {
	OperationID string              `yaml:"operationId"`
	Summary     string              `yaml:"summary"`
	Parameters  []Parameter         `yaml:"parameters"`
	Responses   map[string]Response `yaml:"responses"`
}

type Parameter struct {
	Name     string      `yaml:"name"`
	In       string      `yaml:"in"` // "path" or "query"
	Required bool        `yaml:"required"`
	Schema   ParamSchema `yaml:"schema"`
}

type ParamSchema struct {
	Type   string   `yaml:"type"`
	Format string   `yaml:"format"`
	Enum   []string `yaml:"enum"`
}

// HasEnum reports whether the spec constrains this parameter to a fixed set
// of values.
func (p Parameter) HasEnum() bool { return len(p.Schema.Enum) > 0 }

type Response struct {
	Description string               `yaml:"description"`
	Content     map[string]MediaType `yaml:"content"`
}

type MediaType struct {
	Schema SchemaObj `yaml:"schema"`
}

// SchemaObj is a (partial) JSON Schema node: either a $ref, or an inline
// object/array/scalar description. Properties and Items are themselves
// SchemaObj so a schema tree can be walked and $refs resolved lazily against
// Components.
type SchemaObj struct {
	Ref        string               `yaml:"$ref"`
	Type       string               `yaml:"type"`
	Properties map[string]SchemaObj `yaml:"properties"`
	Items      *SchemaObj           `yaml:"items"`
	Required   []string             `yaml:"required"`
}

type Components struct {
	Schemas map[string]SchemaObj `yaml:"schemas"`
}

// refName extracts "Namespace" out of "#/components/schemas/Namespace".
func refName(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ref
	}
	return ref[i+1:]
}

// Resolve follows s.Ref (if any) against the component schemas, returning
// the concrete schema to inspect. Schemas with no $ref are returned as-is.
func (c Components) Resolve(s SchemaObj) SchemaObj {
	seen := map[string]bool{}
	for s.Ref != "" {
		name := refName(s.Ref)
		if seen[name] {
			break // defend against a cyclic spec; not expected here
		}
		seen[name] = true
		next, ok := c.Schemas[name]
		if !ok {
			break
		}
		s = next
	}
	return s
}

// LoadSpec parses the OpenAPI document at path.
func LoadSpec(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The overwhelmingly likely cause is a checkout whose spec
			// submodule was never initialised, which otherwise reads as
			// "the spec is missing" rather than "fetch it".
			return nil, fmt.Errorf("conformance: the spec is not present at %s — "+
				"it is a git submodule, so run `git submodule update --init --recursive`: %w", path, err)
		}
		return nil, fmt.Errorf("conformance: reading spec: %w", err)
	}
	var spec Spec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("conformance: parsing spec: %w", err)
	}
	if len(spec.Paths) == 0 {
		return nil, fmt.Errorf("conformance: spec at %s declares no paths", path)
	}
	return &spec, nil
}

// Endpoint names one operation: a path template plus its HTTP method
// ("GET"), matching how the suite classifies and reports on it.
type Endpoint struct {
	Path   string
	Method string
	Op     Operation
}

// Endpoints returns every operation in the spec, sorted for deterministic
// test output.
func (s *Spec) Endpoints() []Endpoint {
	var out []Endpoint
	for path, item := range s.Paths {
		for method, op := range item {
			out = append(out, Endpoint{Path: path, Method: strings.ToUpper(method), Op: op})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Key identifies the endpoint the way the suite's classified list does.
func (e Endpoint) Key() string { return e.Method + " " + e.Path }

// PathParams returns the parameters declared with in: path.
func (e Endpoint) PathParams() []Parameter {
	var out []Parameter
	for _, p := range e.Op.Parameters {
		if p.In == "path" {
			out = append(out, p)
		}
	}
	return out
}

// QueryParams returns the parameters declared with in: query.
func (e Endpoint) QueryParams() []Parameter {
	var out []Parameter
	for _, p := range e.Op.Parameters {
		if p.In == "query" {
			out = append(out, p)
		}
	}
	return out
}

// SuccessDataSchema returns the (resolved) schema of the "data" property of
// this endpoint's 200 response, i.e. what the envelope's payload must look
// like. ok is false when the endpoint documents no such shape (nothing to
// check against).
func (e Endpoint) SuccessDataSchema(c Components) (SchemaObj, bool) {
	resp, ok := e.Op.Responses["200"]
	if !ok {
		return SchemaObj{}, false
	}
	media, ok := resp.Content["application/json"]
	if !ok {
		return SchemaObj{}, false
	}
	envelope := c.Resolve(media.Schema)
	data, ok := envelope.Properties["data"]
	if !ok {
		return SchemaObj{}, false
	}
	return c.Resolve(data), true
}
