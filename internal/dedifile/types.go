// Package dedifile builds and signs the file-publication artifacts defined
// by docs/spec/lfdt/docs/publishing-dedi-files.md: one signed DeDi file per
// (namespace, registry) and a signed manifest at
// /.well-known/dedi.index.json listing them.
//
// This is the producer half only. The node still answers /dedi/lookup and
// /dedi/query directly (the API surface); these artifacts are a second,
// static-file projection of the same log, for a standard-conformant crawler
// that ingests files rather than calling an API.
package dedifile

import "encoding/json"

// DediVersion is the file-format version this package emits.
const DediVersion = "0.1"

// JWK is the public half of an RFC 7517 JSON Web Key. Only the OKP/Ed25519
// fields are populated; the others exist so a struct literal matches the
// schema's permissive additionalProperties:true shape without extra work.
type JWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
}

// Proof is a detached JWS over the JCS canonicalization of the document with
// this field removed. See docs/publishing-dedi-files.md section 7.
type Proof struct {
	VerificationMethod string `json:"verification_method"`
	Canonicalization   string `json:"canonicalization"`
	JWS                string `json:"jws"`
}

// Publisher identifies the domain that signed a DeDi file and embeds the
// public key a verifier checks the signature against.
type Publisher struct {
	Domain string `json:"domain"`
	Key    JWK    `json:"key"`
}

// Registry carries the registry-level metadata a DeDi file's records belong
// to.
type Registry struct {
	Name      string `json:"name"`
	Schema    any    `json:"schema"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

// Record is one entry in a DeDi file: a name and its schema-conformant
// payload, carried verbatim.
type Record struct {
	RecordName string          `json:"record_name"`
	Details    json.RawMessage `json:"details"`
}

// File is a self-contained, signed DeDi file: one registry and its records.
// Field names mirror the DeDi API's own so a server can project this
// straight into a /dedi/lookup response.
type File struct {
	DediVersion string    `json:"dedi_version"`
	Type        string    `json:"type"` // always "dedi-file"
	SourceURL   string    `json:"source_url"`
	NextUpdate  string    `json:"next_update"`
	Publisher   Publisher `json:"publisher"`
	Namespace   string    `json:"namespace"`
	Registry    Registry  `json:"registry"`
	Records     []Record  `json:"records"`
	Proof       Proof     `json:"proof,omitempty"`
}

// ManifestEntry references one DeDi file the manifest offers. Only the
// referenced form is produced here (never the inline form): every registry
// on this node, however small, is served as its own file at a stable URL.
type ManifestEntry struct {
	Registry string `json:"registry"`
	URL      string `json:"url"`
	Digest   string `json:"digest"` // "sha-256:<hex>" over the referenced file's exact bytes
	Schema   any    `json:"schema,omitempty"`
	State    string `json:"state,omitempty"`
}

// Manifest is the signed document served at /.well-known/dedi.index.json.
// It is the trust anchor: a DeDi file is authentic only if its
// publisher.key appears in this manifest's keys.
type Manifest struct {
	DediVersion string          `json:"dedi_version"`
	Type        string          `json:"type"` // "dedi-manifest"
	Domain      string          `json:"domain"`
	Name        string          `json:"name,omitempty"`
	Keys        []JWK           `json:"keys"`
	UpdatedAt   string          `json:"updated_at"`
	NextUpdate  string          `json:"next_update"`
	Files       []ManifestEntry `json:"files"`
	Proof       Proof           `json:"proof,omitempty"`
}
