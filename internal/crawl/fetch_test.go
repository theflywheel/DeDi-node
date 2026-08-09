package crawl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
)

// publisher is a stand-in for another operator's site: it holds a keypair and
// serves a signed manifest plus its files, so these tests exercise the real
// verification path rather than a mock of it.
type publisher struct {
	t      *testing.T
	priv   ed25519.PrivateKey
	kid    string
	domain string
	srv    *httptest.Server
	files  map[string]dedifile.File // registry name -> file
	// tamper, when set, rewrites a response body just before it is served.
	tamper func(path string, raw []byte) []byte
}

func newPublisher(t *testing.T, domain string) *publisher {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := &publisher{t: t, priv: priv, kid: "key-1", domain: domain, files: map[string]dedifile.File{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	// A publisher speaks for the host it is actually served from, which for an
	// httptest server is only known once it is listening. Files signed before
	// this point would carry a publisher.domain the manifest disagrees with —
	// which is exactly the mismatch fetchFile is there to catch.
	p.domain = p.origin()
	return p
}

// origin is what the crawler is pointed at. httptest hands out a 127.0.0.1
// address, so tests set AllowPrivateTargets — the guard itself is asserted
// separately in TestFetchRefusesAPrivateTarget.
func (p *publisher) origin() string { return strings.TrimPrefix(p.srv.URL, "http://") }

// publish adds or replaces one registry's records.
func (p *publisher) publish(namespace, registry string, records []dedifile.Record, nextUpdate time.Time) {
	p.t.Helper()
	pub := p.priv.Public().(ed25519.PublicKey)
	f := dedifile.File{
		DediVersion: dedifile.DediVersion,
		Type:        "dedi-file",
		SourceURL:   p.srv.URL + "/dedi-files/" + namespace + "/dedi." + registry + ".json",
		NextUpdate:  nextUpdate.UTC().Format(time.RFC3339),
		Publisher:   dedifile.Publisher{Domain: p.domain, Key: dedifile.PublicJWK(p.kid, pub)},
		Namespace:   namespace,
		Registry: dedifile.Registry{
			Name: registry, Schema: map[string]any{"type": "object"},
			State: "live", UpdatedAt: nextUpdate.Add(-time.Hour).UTC().Format(time.RFC3339),
		},
		Records: records,
	}
	signed, err := dedifile.SignFile(f, p.priv, p.kid)
	if err != nil {
		p.t.Fatal(err)
	}
	p.files[registry] = signed
}

func (p *publisher) manifest(nextUpdate time.Time) dedifile.Manifest {
	p.t.Helper()
	pub := p.priv.Public().(ed25519.PublicKey)
	m := dedifile.Manifest{
		DediVersion: dedifile.DediVersion,
		Type:        "dedi-manifest",
		Domain:      p.domain,
		Keys:        []dedifile.JWK{dedifile.PublicJWK(p.kid, pub)},
		UpdatedAt:   nextUpdate.Add(-time.Hour).UTC().Format(time.RFC3339),
		NextUpdate:  nextUpdate.UTC().Format(time.RFC3339),
	}
	for _, name := range sortedRegistries(p.files) {
		f := p.files[name]
		raw, err := json.Marshal(f)
		if err != nil {
			p.t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		m.Files = append(m.Files, dedifile.ManifestEntry{
			Registry: name, URL: f.SourceURL,
			Digest: "sha-256:" + hex.EncodeToString(sum[:]),
			Schema: f.Registry.Schema, State: f.Registry.State,
		})
	}
	signed, err := dedifile.SignManifest(m, p.priv, p.kid)
	if err != nil {
		p.t.Fatal(err)
	}
	return signed
}

func sortedRegistries(m map[string]dedifile.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// nextUpdate is the freshness the publisher stamps on everything it serves.
var testNextUpdate = time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)

func (p *publisher) serve(w http.ResponseWriter, r *http.Request) {
	var body any
	switch {
	case r.URL.Path == "/.well-known/dedi.index.json":
		body = p.manifest(testNextUpdate)
	case strings.HasPrefix(r.URL.Path, "/dedi-files/"):
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "dedi."), ".json")
		f, ok := p.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body = f
	default:
		http.NotFound(w, r)
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		p.t.Fatal(err)
	}
	if p.tamper != nil {
		raw = p.tamper(r.URL.Path, raw)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// fetcher is a crawler frozen at a time when the publisher's documents are
// fresh, permitted to reach the loopback test server.
func fetcher() *Fetcher {
	return &Fetcher{
		AllowPrivateTargets: true,
		Now:                 func() time.Time { return testNextUpdate.Add(-time.Hour) },
	}
}

func TestFetchVerifiesAWholeDirectory(t *testing.T) {
	p := newPublisher(t, "example.org")
	p.publish("example.org", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
		{RecordName: "gateway", Details: json.RawMessage(`{"publicKey":"def"}`)},
	}, testNextUpdate)

	res, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Files) != 1 || len(res.Files[0].Records) != 2 {
		t.Fatalf("crawled %d files", len(res.Files))
	}
	if res.KeyID != "key-1" {
		t.Fatalf("KeyID = %q", res.KeyID)
	}
}

// TestFetchRejectsTamperedBytes is the point of the whole exercise: §7.3 has a
// verifier check the manifest's digest against the served bytes and the file's
// own signature. Both must fail closed, and neither may be satisfied by an
// attacker who can rewrite responses in flight.
func TestFetchRejectsTamperedBytes(t *testing.T) {
	cases := map[string]func(p *publisher){
		"file body rewritten after signing": func(p *publisher) {
			p.tamper = func(path string, raw []byte) []byte {
				if strings.HasPrefix(path, "/dedi-files/") {
					return []byte(strings.Replace(string(raw), `"abc"`, `"attacker"`, 1))
				}
				return raw
			}
		},
		"manifest signature stripped": func(p *publisher) {
			p.tamper = func(path string, raw []byte) []byte {
				if path == "/.well-known/dedi.index.json" {
					var m map[string]any
					json.Unmarshal(raw, &m)
					m["domain"] = "attacker.example"
					out, _ := json.Marshal(m)
					return out
				}
				return raw
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPublisher(t, "example.org")
			p.publish("example.org", "keys", []dedifile.Record{
				{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
			}, testNextUpdate)
			setup(p)
			if _, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), ""); err == nil {
				t.Fatal("tampered directory was accepted")
			}
		})
	}
}

// TestFetchRejectsAKeyChange is the takeover case §14 leaves open. A rotation
// and a compromise look identical from outside, so a pinned crawler stops and
// asks rather than ingesting whatever the new key vouches for.
func TestFetchRejectsAKeyChange(t *testing.T) {
	p := newPublisher(t, "example.org")
	p.publish("example.org", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"abc"}`)},
	}, testNextUpdate)

	res, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), "")
	if err != nil {
		t.Fatal(err)
	}
	pinned := res.Manifest.Keys[0].X

	// Same pin, same key: still fine.
	if _, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), pinned); err != nil {
		t.Fatalf("re-crawl with the same pin failed: %v", err)
	}

	// New keypair, everything re-signed consistently — internally valid, and
	// exactly what a takeover looks like.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	p.priv = priv
	p.publish("example.org", "keys", []dedifile.Record{
		{RecordName: "auth", Details: json.RawMessage(`{"publicKey":"attacker"}`)},
	}, testNextUpdate)

	_, err = fetcher().Fetch(context.Background(), "http://"+p.origin(), pinned)
	if !errors.Is(err, ErrKeyChanged) {
		t.Fatalf("want ErrKeyChanged, got %v", err)
	}
}

// TestFetchRejectsStaleDocuments: §13 requires a server to honour freshness,
// and a document past its own next_update is one the publisher has already
// declared expired.
func TestFetchRejectsStaleDocuments(t *testing.T) {
	p := newPublisher(t, "example.org")
	p.publish("example.org", "keys", nil, testNextUpdate)

	f := fetcher()
	f.Now = func() time.Time { return testNextUpdate.Add(time.Minute) }
	_, err := f.Fetch(context.Background(), "http://"+p.origin(), "")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
}

// TestFetchRejectsAnOffOriginFileURL: without this, any domain we crawl could
// list a file hosted somewhere it does not control and have that content filed
// under its own name — or point us at an internal address and use the crawler
// as a network probe.
func TestFetchRejectsAnOffOriginFileURL(t *testing.T) {
	p := newPublisher(t, "example.org")
	p.publish("example.org", "keys", nil, testNextUpdate)
	p.tamper = func(path string, raw []byte) []byte {
		if path == "/.well-known/dedi.index.json" {
			return []byte(strings.Replace(string(raw), p.srv.URL+"/dedi-files/", "https://attacker.example/dedi-files/", 1))
		}
		return raw
	}
	if _, err := fetcher().Fetch(context.Background(), "http://"+p.origin(), ""); err == nil {
		t.Fatal("a manifest pointing off-origin was accepted")
	}
}

// TestFetchRefusesAPrivateTarget guards the SSRF surface a crawler inherently
// is. Same posture as webhook delivery, and for the same reason: on every major
// cloud the link-local metadata address hands out instance credentials.
func TestFetchRefusesAPrivateTarget(t *testing.T) {
	f := &Fetcher{Now: func() time.Time { return testNextUpdate.Add(-time.Hour) }}
	_, err := f.Fetch(context.Background(), "http://127.0.0.1:1", "")
	if err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("want a not-public refusal, got %v", err)
	}
}
