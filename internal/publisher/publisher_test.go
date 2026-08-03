package publisher

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T, kid, ns string) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, kid + ":" + ns + ":" + base64.StdEncoding.EncodeToString(pub)
}

func mustSet(t *testing.T, spec string) *KeySet {
	t.Helper()
	ks, err := ParseKeySet(spec)
	if err != nil {
		t.Fatalf("ParseKeySet: %v", err)
	}
	return ks
}

func TestSignedRequestVerifies(t *testing.T) {
	priv, entry := testKey(t, "op-1", "beckn-testnet")
	ks := mustSet(t, entry)
	now := time.Now()
	body := []byte(`{"subscriber_id":"bpp.example.com"}`)
	uri := "/admin/records/x:publish?expected_version=0&state=revoked"
	sig := Sign(priv, "POST", uri, body, now)

	key, err := ks.Verify("POST", uri, body, "op-1", now.Format(time.RFC3339), sig, now, DefaultMaxSkew)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if key.KID != "op-1" || key.Namespace != "beckn-testnet" {
		t.Fatalf("key = %+v", key)
	}
}

// Each of these tampers with exactly one thing the signature covers.
func TestVerifyRejectsTampering(t *testing.T) {
	priv, entry := testKey(t, "op-1", "ns")
	ks := mustSet(t, entry)
	now := time.Now()
	ts := now.Format(time.RFC3339)
	body := []byte(`{"a":1}`)
	good := Sign(priv, "POST", "/admin/x", body, now)

	cases := []struct {
		name                     string
		method, path, kid, tsHdr string
		body                     []byte
		sig                      string
		want                     error
	}{
		{name: "body changed", method: "POST", path: "/admin/x", body: []byte(`{"a":2}`), kid: "op-1", tsHdr: ts, sig: good, want: ErrBadSignature},
		{name: "path changed", method: "POST", path: "/admin/y", body: body, kid: "op-1", tsHdr: ts, sig: good, want: ErrBadSignature},
		{name: "query added", method: "POST", path: "/admin/x?state=revoked", body: body, kid: "op-1", tsHdr: ts, sig: good, want: ErrBadSignature},
		{name: "method changed", method: "DELETE", path: "/admin/x", body: body, kid: "op-1", tsHdr: ts, sig: good, want: ErrBadSignature},
		{name: "timestamp changed", method: "POST", path: "/admin/x", body: body, kid: "op-1", tsHdr: now.Add(time.Minute).Format(time.RFC3339), sig: good, want: ErrBadSignature},
		{name: "unknown key id", method: "POST", path: "/admin/x", body: body, kid: "nope", tsHdr: ts, sig: good, want: ErrUnknownKey},
		{name: "garbage signature", method: "POST", path: "/admin/x", body: body, kid: "op-1", tsHdr: ts, sig: base64.StdEncoding.EncodeToString([]byte("nope")), want: ErrBadSignature},
		{name: "signature not base64", method: "POST", path: "/admin/x", body: body, kid: "op-1", tsHdr: ts, sig: "!!!", want: ErrMalformedHead},
		{name: "timestamp not rfc3339", method: "POST", path: "/admin/x", body: body, kid: "op-1", tsHdr: "yesterday", sig: good, want: ErrMalformedHead},
		{name: "no headers at all", method: "POST", path: "/admin/x", body: body, want: ErrNoSignature},
		{name: "partial headers", method: "POST", path: "/admin/x", body: body, kid: "op-1", want: ErrMalformedHead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ks.Verify(tc.method, tc.path, tc.body, tc.kid, tc.tsHdr, tc.sig, now, DefaultMaxSkew)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A signature from a key the node does not know must not verify even though it
// is cryptographically valid — this is the "attacker signs their own request"
// case, distinct from a forged signature.
func TestVerifyRejectsForeignKey(t *testing.T) {
	_, entry := testKey(t, "op-1", "ns")
	ks := mustSet(t, entry)
	attacker, _ := testKey(t, "op-1", "ns") // same kid, different key material
	now := time.Now()
	body := []byte(`{}`)
	sig := Sign(attacker, "POST", "/admin/x", body, now)

	if _, err := ks.Verify("POST", "/admin/x", body, "op-1", now.Format(time.RFC3339), sig, now, DefaultMaxSkew); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsStaleAndFutureTimestamps(t *testing.T) {
	priv, entry := testKey(t, "op-1", "ns")
	ks := mustSet(t, entry)
	now := time.Now()
	body := []byte(`{}`)

	for _, offset := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		ts := now.Add(offset)
		sig := Sign(priv, "POST", "/admin/x", body, ts)
		_, err := ks.Verify("POST", "/admin/x", body, "op-1", ts.Format(time.RFC3339), sig, now, DefaultMaxSkew)
		if !errors.Is(err, ErrStale) {
			t.Fatalf("offset %v: err = %v, want ErrStale", offset, err)
		}
	}
	// Inside the window it must still verify, including a clock slightly ahead.
	ts := now.Add(2 * time.Minute)
	sig := Sign(priv, "POST", "/admin/x", body, ts)
	if _, err := ks.Verify("POST", "/admin/x", body, "op-1", ts.Format(time.RFC3339), sig, now, DefaultMaxSkew); err != nil {
		t.Fatalf("within skew: %v", err)
	}
}

func TestKeyScoping(t *testing.T) {
	k := Key{KID: "op-1", Namespace: "beckn-testnet"}
	if err := k.Authorizes("beckn-testnet"); err != nil {
		t.Fatalf("own namespace: %v", err)
	}
	if err := k.Authorizes("other-net"); !errors.Is(err, ErrWrongScope) {
		t.Fatalf("err = %v, want ErrWrongScope", err)
	}
}

func TestParseKeySet(t *testing.T) {
	_, a := testKey(t, "a", "ns1")
	_, b := testKey(t, "b", "ns2")
	ks := mustSet(t, a+", "+b)
	if ks.Len() != 2 {
		t.Fatalf("len = %d", ks.Len())
	}
	if k, ok := ks.Lookup("b"); !ok || k.Namespace != "ns2" {
		t.Fatalf("lookup b = %+v %v", k, ok)
	}
	// Empty spec must yield an empty set: no keys means writes stay closed.
	if empty := mustSet(t, ""); empty.Len() != 0 {
		t.Fatalf("empty spec produced %d keys", empty.Len())
	}
	for _, bad := range []string{"a:ns", "a:ns:!!!", "a:ns:" + base64.StdEncoding.EncodeToString([]byte("short")), ":ns:AAAA", a + "," + a} {
		if _, err := ParseKeySet(bad); !errors.Is(err, ErrMalformedKey) {
			t.Fatalf("ParseKeySet(%q) err = %v, want ErrMalformedKey", bad, err)
		}
	}
}

func TestMiddlewareAllowsAndRestoresBody(t *testing.T) {
	priv, entry := testKey(t, "op-1", "ns")
	auth := &Authenticator{Keys: mustSet(t, entry)}
	var seenBody string
	var seenKID string
	h := auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		if k, ok := KeyFrom(r.Context()); ok {
			seenKID = k.KID
		}
		w.WriteHeader(http.StatusNoContent)
	}), func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, err.Error(), StatusFor(err))
	})

	body := `{"hello":"world"}`
	now := time.Now()
	req := httptest.NewRequest("POST", "/admin/x?expected_version=0&state=live", strings.NewReader(body))
	req.Header.Set(HeaderKeyID, "op-1")
	req.Header.Set(HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(HeaderSignature, Sign(priv, "POST", "/admin/x?expected_version=0&state=live", []byte(body), now))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if seenBody != body {
		t.Fatalf("handler saw body %q, want %q — middleware must restore it", seenBody, body)
	}
	if seenKID != "op-1" {
		t.Fatalf("handler saw key %q", seenKID)
	}
}

func TestMiddlewareRejectsUnsigned(t *testing.T) {
	_, entry := testKey(t, "op-1", "ns")
	auth := &Authenticator{Keys: mustSet(t, entry)}
	reached := false
	h := auth.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }),
		func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, err.Error(), StatusFor(err)) })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/x", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if reached {
		t.Fatal("handler ran for an unsigned request")
	}
}

// An empty key set is the default: with no DEDI_PUBLISHER_KEYS configured the
// write plane must reject everything rather than fall open.
func TestEmptyKeySetRejectsEverything(t *testing.T) {
	priv, _ := testKey(t, "op-1", "ns")
	ks := mustSet(t, "")
	now := time.Now()
	sig := Sign(priv, "POST", "/admin/x", []byte(`{}`), now)
	if _, err := ks.Verify("POST", "/admin/x", []byte(`{}`), "op-1", now.Format(time.RFC3339), sig, now, DefaultMaxSkew); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestMiddlewareRejectsOversizedBody(t *testing.T) {
	_, entry := testKey(t, "op-1", "ns")
	auth := &Authenticator{Keys: mustSet(t, entry)}
	h := auth.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler ran") }),
		func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, err.Error(), StatusFor(err)) })

	big := bytes.Repeat([]byte("x"), MaxBodyBytes+1)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/x", bytes.NewReader(big)))
	if rec.Code == http.StatusNoContent {
		t.Fatal("oversized body was accepted")
	}
}
