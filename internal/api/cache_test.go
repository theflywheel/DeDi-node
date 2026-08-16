package api

import (
	"net/http"
	"testing"
)

func TestEffectiveTTL(t *testing.T) {
	cases := []struct {
		payload string
		def     int
		want    int
	}{
		{`{"a":1}`, 300, 300},         // no ttl declared
		{`{"ttl":30}`, 300, 30},       // record overrides the node default
		{`{"ttl":0}`, 300, 300},       // zero is meaningless
		{`{"ttl":-5}`, 300, 300},      // negative is meaningless
		{`{"ttl":1.5}`, 300, 300},     // fractional seconds are meaningless
		{`{"ttl":"soon"}`, 300, 300},  // wrong type
		{`not json`, 300, 300},        // unparseable payload
		{`{"ttl":86400}`, 300, 86400}, // long TTLs are the operator's call
	}
	for _, tc := range cases {
		if got := effectiveTTL([]byte(tc.payload), tc.def); got != tc.want {
			t.Fatalf("effectiveTTL(%s) = %d, want %d", tc.payload, got, tc.want)
		}
	}
}

// A record's declared ttl is what reaches ONIX, and it is what bounds how long
// a revoked participant keeps validating signatures.
func TestRecordTTLReachesTheResponse(t *testing.T) {
	srv, _, priv := writeServer(t, "beckn-testnet")
	path := setupRegistry(t, srv, priv, "beckn-testnet", "subscribers.beckn.one", "KEY-FAST")
	signedDo(t, srv, priv, "POST", path+"/publish",
		[]byte(`{"payload":{"subscriber_id":"fast.example","type":"BPP","ttl":30}}`)).Body.Close()

	m := getJSON(t, srv.URL+"/dedi/lookup/fast.example/subscribers.beckn.one/KEY-FAST", http.StatusOK)
	if ttl := m["data"].(map[string]any)["ttl"].(float64); ttl != 30 {
		t.Fatalf("ttl = %v, want the record's declared 30", ttl)
	}
}

func TestLookupETagAndRevalidation(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"v":1,"ttl":30}}`)).Body.Close()

	url := srv.URL + "/dedi/lookup/ns/r/KEY-1"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a lookup")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=30" {
		t.Fatalf("Cache-Control = %q, want the record's ttl", cc)
	}

	// Unchanged: revalidation is a 304.
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("unchanged record: status %d, want 304", resp.StatusCode)
	}

	// After a new version the old tag must no longer match.
	signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"v":2,"ttl":30}}`)).Body.Close()
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changed record: status %d, want 200", resp.StatusCode)
	}
}

// Revocation must break the ETag, or a revalidating cache would keep serving a
// revoked participant as current.
func TestRevocationChangesETag(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"subscriber_id":"a"}}`)).Body.Close()

	resp, _ := http.Get(srv.URL + "/dedi/lookup/ns/r/KEY-1")
	resp.Body.Close()
	before := resp.Header.Get("ETag")

	signedDo(t, srv, priv, "POST", path+"/revoke", []byte(`{}`)).Body.Close()
	resp, _ = http.Get(srv.URL + "/dedi/lookup/ns/r/KEY-1")
	resp.Body.Close()
	if after := resp.Header.Get("ETag"); after == before {
		t.Fatalf("ETag unchanged across revocation (%s) — a cache would keep serving it", after)
	}
}

// Version-pinned reads can never change, so they are immutable.
func TestPinnedReadIsImmutable(t *testing.T) {
	srv, _, priv := writeServer(t, "ns")
	path := setupRegistry(t, srv, priv, "ns", "r", "KEY-1")
	body := bodyOf(t, signedDo(t, srv, priv, "POST", path+"/publish", []byte(`{"payload":{"v":1}}`)))
	version := body["data"].(map[string]any)["version"].(string)

	resp, err := http.Get(srv.URL + "/dedi/lookup/ns/r/KEY-1?version_id=" + version)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("pinned read Cache-Control = %q", cc)
	}
}
