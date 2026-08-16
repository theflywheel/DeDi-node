package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func schemaOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidateAgainstSchema(t *testing.T) {
	participant := schemaOf(t, `{
		"type": "object",
		"required": ["subscriber_id", "signing_public_key"],
		"properties": {
			"subscriber_id": {"type": "string"},
			"network_memberships": {"type": "array"},
			"ttl": {"type": "integer"}
		}
	}`)

	cases := []struct {
		name    string
		payload string
		wantErr string // substring; empty means it must pass
	}{
		{name: "valid", payload: `{"subscriber_id":"bpp.example.com","signing_public_key":"k"}`},
		{name: "extra fields are fine", payload: `{"subscriber_id":"a","signing_public_key":"k","anything":1}`},
		{name: "null is not an object", payload: `null`, wantErr: "payload must be a JSON object"},
		{name: "missing one required", payload: `{"subscriber_id":"a"}`, wantErr: "signing_public_key"},
		{name: "missing several, listed", payload: `{}`, wantErr: "signing_public_key, subscriber_id"},
		{name: "wrong scalar type", payload: `{"subscriber_id":42,"signing_public_key":"k"}`, wantErr: `"subscriber_id" must be string`},
		{name: "wrong array type", payload: `{"subscriber_id":"a","signing_public_key":"k","network_memberships":"nope"}`, wantErr: "must be array"},
		{name: "integer accepts whole number", payload: `{"subscriber_id":"a","signing_public_key":"k","ttl":300}`},
		{name: "integer rejects fraction", payload: `{"subscriber_id":"a","signing_public_key":"k","ttl":1.5}`, wantErr: "must be integer"},
		{name: "absent optional is fine", payload: `{"subscriber_id":"a","signing_public_key":"k"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgainstSchema(participant, []byte(tc.payload))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidWrite) {
				t.Fatalf("error is not ErrInvalidWrite: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// Registries that declare nothing must keep accepting anything — nodes seeded
// before schemas existed cannot start failing.
func TestValidateAgainstSchemaEmptyAcceptsAll(t *testing.T) {
	for _, schema := range []map[string]any{nil, {}} {
		if err := ValidateAgainstSchema(schema, []byte(`{"whatever":true}`)); err != nil {
			t.Fatalf("empty schema rejected a payload: %v", err)
		}
	}
}

// Keywords outside the supported subset are ignored, not rejected: a richer
// schema stays writable, it is simply not fully enforced.
func TestValidateAgainstSchemaIgnoresUnsupportedKeywords(t *testing.T) {
	s := schemaOf(t, `{"type":"object","minProperties":5,"properties":{"a":{"type":"string","minLength":10}}}`)
	if err := ValidateAgainstSchema(s, []byte(`{"a":"short"}`)); err != nil {
		t.Fatalf("unsupported keyword caused a rejection: %v", err)
	}
}

func TestAppendEnforcesRegistrySchema(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "ns", Registry: "participants", CreatedBy: "t",
		PayloadRaw: []byte(`{"schema":{"type":"object","required":["subscriber_id","signing_public_key"]}}`)})

	// Missing a required field: rejected, and as a caller error.
	_, err := s.Append(ctx, AppendInput{EntryType: "record", Namespace: "ns", Registry: "participants",
		RecordName: "rec-1", PayloadRaw: []byte(`{"subscriber_id":"a"}`), CreatedBy: "t"})
	if !errors.Is(err, ErrInvalidWrite) {
		t.Fatalf("err = %v, want ErrInvalidWrite", err)
	}
	if !strings.Contains(err.Error(), "signing_public_key") {
		t.Fatalf("error should name the missing field: %v", err)
	}

	// Complete payload: accepted.
	if _, err := s.Append(ctx, AppendInput{EntryType: "record", Namespace: "ns", Registry: "participants",
		RecordName: "rec-1", PayloadRaw: []byte(`{"subscriber_id":"a","signing_public_key":"k"}`), CreatedBy: "t"}); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

func TestAppendRejectsNullRecordAgainstObjectSchema(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "ns", Registry: "objects", CreatedBy: "t",
		PayloadRaw: []byte(`{"schema":{"type":"object"}}`)})

	_, err := s.Append(ctx, AppendInput{EntryType: "record", Namespace: "ns", Registry: "objects",
		RecordName: "rec-1", PayloadRaw: []byte(`null`), CreatedBy: "t"})
	if !errors.Is(err, ErrInvalidWrite) {
		t.Fatalf("err = %v, want ErrInvalidWrite", err)
	}
	if !strings.Contains(err.Error(), "payload must be a JSON object") {
		t.Fatalf("error should reject null object payload: %v", err)
	}
}

// A registry with no schema keeps accepting whatever it did before.
func TestAppendWithoutSchemaAcceptsAnything(t *testing.T) {
	s := testStore(t)
	mustAppend(t, s, AppendInput{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "registry", Namespace: "ns", Registry: "free", PayloadRaw: []byte(`{}`), CreatedBy: "t"})
	mustAppend(t, s, AppendInput{EntryType: "record", Namespace: "ns", Registry: "free", RecordName: "r",
		PayloadRaw: []byte(`{"anything":"goes"}`), CreatedBy: "t"})
}
