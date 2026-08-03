package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Registries may declare a `schema` in their payload; record payloads written
// to that registry are validated against it. Without this a console or a
// publisher can put anything in a participant record — a missing
// signing_public_key only surfaces later as an ONIX signature failure, far from
// the write that caused it.
//
// This implements a deliberate subset of JSON Schema — `required` and
// `properties.<field>.type` — rather than pulling in a validator dependency.
// It is the subset the registries in this repo actually declare. Keywords
// outside the subset are ignored rather than rejected, so a richer schema stays
// forward-compatible: it just is not fully enforced. Anything relying on
// stricter keywords should not assume the node checks them.

// jsonTypeOf reports the JSON Schema type name for a decoded value.
func jsonTypeOf(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		_ = t
		return "unknown"
	}
}

// typeMatches allows the JSON Schema "integer" type for whole numbers, since
// JSON itself has only "number".
func typeMatches(want, got string, v any) bool {
	if want == got {
		return true
	}
	if want == "integer" && got == "number" {
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	}
	return false
}

// ValidateAgainstSchema checks a record payload against a registry schema.
// An empty or absent schema accepts everything, which is what registries that
// declare none get.
func ValidateAgainstSchema(schema map[string]any, payloadRaw []byte) error {
	if len(schema) == 0 {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return fmt.Errorf("%w: payload must be a JSON object to validate against the registry schema", ErrInvalidWrite)
	}
	if payload == nil {
		return fmt.Errorf("%w: payload must be a JSON object to validate against the registry schema", ErrInvalidWrite)
	}

	if t, ok := schema["type"].(string); ok && t != "object" {
		// The node only stores object payloads, so a registry demanding
		// anything else is a schema we cannot satisfy — say so plainly.
		return fmt.Errorf("%w: registry schema requires type %q, but record payloads are objects", ErrInvalidWrite, t)
	}

	if req, ok := schema["required"].([]any); ok {
		var missing []string
		for _, f := range req {
			name, isStr := f.(string)
			if !isStr {
				continue
			}
			if _, present := payload[name]; !present {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("%w: payload is missing required field(s): %s",
				ErrInvalidWrite, strings.Join(missing, ", "))
		}
	}

	if props, ok := schema["properties"].(map[string]any); ok {
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names) // deterministic error for multi-field mismatches
		for _, name := range names {
			spec, isObj := props[name].(map[string]any)
			if !isObj {
				continue
			}
			want, hasType := spec["type"].(string)
			if !hasType {
				continue
			}
			v, present := payload[name]
			if !present {
				continue // presence is `required`'s job
			}
			if got := jsonTypeOf(v); !typeMatches(want, got, v) {
				return fmt.Errorf("%w: field %q must be %s, got %s", ErrInvalidWrite, name, want, got)
			}
		}
	}
	return nil
}

// registrySchemaFrom returns the schema declared by the registry a record
// belongs to. A registry that does not exist yet, or declares none, yields nil
// which accepts everything, preserving the behaviour of nodes seeded before
// schemas.
func registrySchemaFrom(ctx context.Context, q queryRower, ns, reg string) (map[string]any, error) {
	var raw []byte
	err := q.QueryRow(ctx,
		`SELECT payload_raw FROM log_entries
		  WHERE entry_type='registry' AND namespace=$1 AND registry=$2
		  ORDER BY version_num DESC LIMIT 1`,
		ns, reg).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p struct {
		Schema map[string]any `json:"schema"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil // an unreadable registry payload declares no schema
	}
	return p.Schema, nil
}

// registrySchema returns the schema declared by the registry a record belongs
// to, outside a caller-managed transaction.
func (s *Store) registrySchema(ctx context.Context, ns, reg string) (map[string]any, error) {
	return registrySchemaFrom(ctx, s.pool, ns, reg)
}
