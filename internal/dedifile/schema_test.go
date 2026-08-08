package dedifile

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	fileSchemaID     = "https://raw.githubusercontent.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/main/schemas/dedi-file.json"
	manifestSchemaID = "https://raw.githubusercontent.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/main/schemas/dedi-manifest.json"
)

// specSchemaPath locates the vendored, read-only spec schemas relative to
// this test file, independent of the package the test runner's working
// directory happens to be.
func specSchemaPath(name string) string {
	return filepath.Join("..", "..", "docs", "spec", "lfdt", "schemas", name)
}

func loadCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	for id, file := range map[string]string{
		fileSchemaID:     "dedi-file.schema.json",
		manifestSchemaID: "dedi-manifest.schema.json",
	} {
		raw, err := os.ReadFile(specSchemaPath(file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if err := c.AddResource(id, bytes.NewReader(raw)); err != nil {
			t.Fatalf("add resource %s: %v", id, err)
		}
	}
	return c
}

func testSigner(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	return priv, "test-key-1"
}

func sampleFile(t *testing.T, priv ed25519.PrivateKey, kid string) File {
	t.Helper()
	f := File{
		DediVersion: DediVersion,
		Type:        "dedi-file",
		SourceURL:   "https://dedi.example.org/dedi-files/example.org/dedi.public-keys.json",
		NextUpdate:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		Publisher: Publisher{
			Domain: "example.org",
			Key:    PublicJWK(kid, priv.Public().(ed25519.PublicKey)),
		},
		Namespace: "example.org",
		Registry: Registry{
			Name:      "public-keys",
			Schema:    "https://raw.githubusercontent.com/LF-Decentralized-Trust-labs/decentralized-directory-protocol/main/schemas/public_key.json",
			State:     "live",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Records: []Record{
			{RecordName: "auth-service", Details: json.RawMessage(`{"public_key_id":"example.org:auth","publicKey":"abc","keyType":"ed25519","keyFormat":"base64"}`)},
		},
	}
	signed, err := SignFile(f, priv, kid)
	if err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	return signed
}

func TestFileValidatesAgainstSchema(t *testing.T) {
	priv, kid := testSigner(t)
	f := sampleFile(t, priv, kid)

	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	c := loadCompiler(t)
	schema, err := c.Compile(fileSchemaID)
	if err != nil {
		t.Fatalf("compile dedi-file schema: %v", err)
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatalf("generated DeDi file does not validate against dedi-file.schema.json: %v", err)
	}
}

func TestManifestValidatesAgainstSchema(t *testing.T) {
	priv, kid := testSigner(t)
	f := sampleFile(t, priv, kid)

	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	sum := "sha-256:0000000000000000000000000000000000000000000000000000000000000000"
	_ = raw

	m := Manifest{
		DediVersion: DediVersion,
		Type:        "dedi-manifest",
		Domain:      "example.org",
		Keys:        []JWK{PublicJWK(kid, priv.Public().(ed25519.PublicKey))},
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		NextUpdate:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		Files: []ManifestEntry{
			{Registry: f.Registry.Name, URL: f.SourceURL, Digest: sum, Schema: f.Registry.Schema, State: f.Registry.State},
		},
	}
	signed, err := SignManifest(m, priv, kid)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	rawM, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(rawM, &doc); err != nil {
		t.Fatal(err)
	}

	c := loadCompiler(t)
	schema, err := c.Compile(manifestSchemaID)
	if err != nil {
		t.Fatalf("compile dedi-manifest schema: %v", err)
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatalf("generated manifest does not validate against dedi-manifest.schema.json: %v", err)
	}
}
