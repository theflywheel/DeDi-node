package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func TestWellKnownIndexAndDedifileRoutes(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()

	putJSON := func(ns, reg, rec, body string) {
		t.Helper()
		var err error
		if reg == "" {
			_, err = s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(body), CreatedBy: "test"})
		} else if rec == "" {
			_, err = s.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: ns, Registry: reg, PayloadRaw: []byte(body), CreatedBy: "test"})
		} else {
			_, err = s.Append(ctx, store.AppendInput{EntryType: "record", Namespace: ns, Registry: reg, RecordName: rec, PayloadRaw: []byte(body), CreatedBy: "test"})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	putJSON("example.org", "", "", `{}`)
	putJSON("example.org", "public-keys", "", `{"schema":{"type":"object"}}`)
	putJSON("example.org", "public-keys", "auth-service", `{"public_key_id":"example.org:auth"}`)

	// Manifest.
	resp, err := http.Get(srv.URL + "/.well-known/dedi.index.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET manifest: status %d, body %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag header")
	}
	var manifest dedifile.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("expected 1 manifest file entry, got %d: %+v", len(manifest.Files), manifest.Files)
	}
	if manifest.Files[0].URL == "" {
		t.Fatal("expected a non-empty file URL")
	}

	// Conditional re-fetch: same ETag must 304.
	req, _ := http.NewRequest("GET", srv.URL+"/.well-known/dedi.index.json", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", resp2.StatusCode)
	}

	// The DeDi file itself.
	resp3, err := http.Get(manifest.Files[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("GET dedi file %s: status %d, body %s", manifest.Files[0].URL, resp3.StatusCode, body)
	}
	var f dedifile.File
	if err := json.NewDecoder(resp3.Body).Decode(&f); err != nil {
		t.Fatal(err)
	}
	if f.Namespace != "example.org" || f.Registry.Name != "public-keys" {
		t.Fatalf("unexpected file identity: %+v", f)
	}
	pub, err := dedifile.PublicKeyFromJWK(manifest.Keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := dedifile.VerifyFile(f, pub); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
	if pub2, err := dedifile.PublicKeyFromJWK(f.Publisher.Key); err != nil || string(pub2) != string(pub) {
		t.Fatalf("file's embedded key must match the manifest's key: %v", err)
	}

	// Unknown registry -> 404, not a panic or 500.
	resp4, err := http.Get(srv.URL + "/dedi-files/example.org/dedi.nonexistent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown registry, got %d", resp4.StatusCode)
	}
}

func TestDedifileHidesInternalNamespace(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: "_witness", PayloadRaw: []byte(`{}`), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: "_witness", Registry: "verdicts", PayloadRaw: []byte(`{}`), CreatedBy: "test"}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/.well-known/dedi.index.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var manifest dedifile.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	for _, e := range manifest.Files {
		if e.Registry == "verdicts" {
			t.Fatalf("internal namespace leaked into the manifest: %+v", manifest.Files)
		}
	}

	resp2, err := http.Get(srv.URL + "/dedi-files/_witness/dedi.verdicts.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected internal namespace's DeDi file to 404, got %d", resp2.StatusCode)
	}
}

// Spec §5.2: a publisher SHOULD NOT advertise a max-age extending beyond the
// file's next_update — an HTTP cache outliving that bound serves copies the
// protocol has already declared stale.
func TestCacheControlNeverOutlivesNextUpdate(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cc, ok := cacheControlUntil(now.Add(30*time.Minute).Format(time.RFC3339), now)
	if !ok || cc != "public, max-age=1800" {
		t.Fatalf("expected a max-age bounded by next_update, got %q (ok=%v)", cc, ok)
	}
	// Already stale, unparseable: no promise at all rather than a bad one.
	if _, ok := cacheControlUntil(now.Add(-time.Second).Format(time.RFC3339), now); ok {
		t.Error("a next_update in the past must not yield a max-age")
	}
	if _, ok := cacheControlUntil("not a timestamp", now); ok {
		t.Error("an unparseable next_update must not yield a max-age")
	}
}

// And end to end: the header a client actually receives must be bounded by the
// next_update inside the document it came with.
func TestServedCacheControlMatchesTheDocument(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: "ns", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
		{EntryType: "registry", Namespace: "ns", Registry: "reg", PayloadRaw: []byte(`{}`), CreatedBy: "t"},
	} {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/.well-known/dedi.index.json", "/dedi-files/ns/dedi.reg.json"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var doc struct {
			NextUpdate string `json:"next_update"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		cc := resp.Header.Get("Cache-Control")
		if cc == "" {
			t.Fatalf("%s: no Cache-Control", path)
		}
		var age int
		if _, err := fmt.Sscanf(cc, "public, max-age=%d", &age); err != nil {
			t.Fatalf("%s: unexpected Cache-Control %q", path, cc)
		}
		nu, err := time.Parse(time.RFC3339, doc.NextUpdate)
		if err != nil {
			t.Fatal(err)
		}
		if expiry := time.Now().Add(time.Duration(age) * time.Second); expiry.After(nu) {
			t.Errorf("%s: cache would outlive next_update by %s", path, expiry.Sub(nu))
		}
	}
}
