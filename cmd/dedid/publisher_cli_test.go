package main

// The CLI mints publisher keys and signs requests; the node verifies them. The
// two sides have to agree on key encoding and on the signing preimage, and
// nothing else in the tree checks that they do.

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/domainproof"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/testdb"
)

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return string(out)
}

// header pulls one "Name: value" line out of the sign output.
func header(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimSpace(strings.TrimPrefix(line, name+": "))
		}
	}
	t.Fatalf("no %q header in:\n%s", name, out)
	return ""
}

func TestPubkeygenAndSignVerifyAgainstKeySet(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "publisher.key")
	bodyFile := filepath.Join(dir, "body.json")
	body := []byte(`{"subscriber_id":"bpp.acme.example","type":"BPP"}`)
	if err := os.WriteFile(bodyFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	gen := capture(t, func() error {
		return pubkeygen([]string{"-kid", "op-1", "-namespace", "beckn-testnet", "-out", keyFile})
	})
	var entry string
	for _, line := range strings.Split(gen, "\n") {
		if strings.HasPrefix(line, "DEDI_PUBLISHER_KEYS=") {
			entry = strings.TrimPrefix(line, "DEDI_PUBLISHER_KEYS=")
		}
	}
	if entry == "" {
		t.Fatalf("pubkeygen printed no config entry:\n%s", gen)
	}
	// The printed entry must be exactly what the node accepts as config.
	ks, err := publisher.ParseKeySet(entry)
	if err != nil {
		t.Fatalf("node cannot parse the entry the CLI printed (%q): %v", entry, err)
	}

	const path = "/admin/records/x/publish?state=live"
	const tag = "b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c-live"
	out := capture(t, func() error {
		return signCmd([]string{"-key", keyFile, "-kid", "op-1", "-method", "POST",
			"-path", path, "-body", bodyFile, "-if-match", tag})
	})
	kid := header(t, out, publisher.HeaderKeyID)
	ts := header(t, out, publisher.HeaderTimestamp)
	sig := header(t, out, publisher.HeaderSignature)

	// The CLI must emit the precondition it signed; a caller who forwards only
	// the signature headers would otherwise send an unverifiable request.
	if got := header(t, out, "If-Match"); got != tag {
		t.Fatalf("If-Match header = %q, want %q", got, tag)
	}
	pre := publisher.Precondition{IfMatch: tag}

	key, err := ks.Verify("POST", path, body, pre, kid, ts, sig, time.Now(), publisher.DefaultMaxSkew)
	if err != nil {
		t.Fatalf("CLI signature does not verify server-side: %v", err)
	}
	if key.Namespace != "beckn-testnet" {
		t.Fatalf("namespace = %q", key.Namespace)
	}
	if err := key.Authorizes("other-net"); err == nil {
		t.Fatal("key authorised a namespace it is not scoped to")
	}
	// One byte of drift in the body must break it.
	if _, err := ks.Verify("POST", path, append(body, ' '), pre, kid, ts, sig, time.Now(), publisher.DefaultMaxSkew); err == nil {
		t.Fatal("tampered body verified")
	}
}

func TestPubkeygenRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-namespace", "ns"}, // no kid
		{"-kid", "op-1"},     // no namespace
		{"-kid", "a:b", "-namespace", "ns", "-out", filepath.Join(dir, "k")},    // separator in kid
		{"-kid", "op-1", "-namespace", "a,b", "-out", filepath.Join(dir, "k2")}, // separator in namespace
	} {
		if err := pubkeygen(args); err == nil {
			t.Fatalf("pubkeygen(%v) succeeded, want error", args)
		}
	}
}

func TestSignRejectsUnusableKeyFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("not-base64"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := signCmd([]string{"-key", bad, "-kid", "op-1", "-path", "/admin/x"}); err == nil {
		t.Fatal("signCmd accepted a malformed key file")
	}
	if err := signCmd([]string{"-key", filepath.Join(dir, "missing"), "-kid", "op-1", "-path", "/admin/x"}); err == nil {
		t.Fatal("signCmd accepted a missing key file")
	}
}

// zone is a fixed TXT zone, so the domain-verify round trip below runs without
// public DNS.
type zone map[string][]string

func (z zone) LookupTXT(_ context.Context, name string) ([]string, error) {
	if txt, ok := z[name]; ok {
		return txt, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// Domain verify takes no precondition, so the CLI has to be able to sign a
// request with none, and the node has to accept exactly what it printed (#79).
func TestSignWithNoPreconditionIsAcceptedByTheNode(t *testing.T) {
	const ns, domain, nodeKey = "crest", "crest.example", "cli-test-node-key"
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "publisher.key")
	gen := capture(t, func() error {
		return pubkeygen([]string{"-kid", "op-1", "-namespace", ns, "-out", keyFile})
	})
	var entry string
	for _, line := range strings.Split(gen, "\n") {
		if strings.HasPrefix(line, "DEDI_PUBLISHER_KEYS=") {
			entry = strings.TrimPrefix(line, "DEDI_PUBLISHER_KEYS=")
		}
	}
	keys, err := publisher.ParseKeySet(entry)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	s, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, store.AppendInput{
		EntryType: "namespace", Namespace: ns, CreatedBy: "seed",
		PayloadRaw: []byte(`{"description":"test","domain":"` + domain + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	name, value := domainproof.Challenge(ns, domain, nodeKey)
	skey, _, err := note.GenerateKey(rand.Reader, "cli.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&api.Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "cli.test/log", Interval: time.Hour},
		TTL: 300, VerifierKey: nodeKey, WildcardNamespaces: []string{ns},
		Auth:        &publisher.Authenticator{Keys: keys},
		DNSResolver: zone{name: {value}},
	}).Handler())
	t.Cleanup(srv.Close)

	path := "/admin/namespaces/" + ns + "/domain/verify"
	out := capture(t, func() error {
		return signCmd([]string{"-key", keyFile, "-kid", "op-1", "-method", "POST", "-path", path})
	})
	if strings.Contains(out, "If-Match") || strings.Contains(out, "If-None-Match") {
		t.Fatalf("no precondition was asked for, but the CLI emitted one:\n%s", out)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{publisher.HeaderKeyID, publisher.HeaderTimestamp, publisher.HeaderSignature} {
		req.Header.Set(h, header(t, out, h))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify signed by the CLI: %d %s", resp.StatusCode, body)
	}

	// Dropping the CLI's insistence is safe only because a route that needs a
	// precondition still refuses a request without one.
	bodyFile := filepath.Join(dir, "ns.json")
	nsBody := []byte(`{"payload":{"description":"edited"}}`)
	if err := os.WriteFile(bodyFile, nsBody, 0o600); err != nil {
		t.Fatal(err)
	}
	path = "/admin/namespaces/" + ns
	out = capture(t, func() error {
		return signCmd([]string{"-key", keyFile, "-kid", "op-1", "-method", "PUT", "-path", path, "-body", bodyFile})
	})
	req, err = http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader(string(nsBody)))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{publisher.HeaderKeyID, publisher.HeaderTimestamp, publisher.HeaderSignature} {
		req.Header.Set(h, header(t, out, h))
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("namespace write with no precondition: %d %s, want 428", resp.StatusCode, body)
	}
}

// Both flags would sign two contradictory preconditions: a write cannot be both
// a create and a replace. The namespace, registry and record writes refuse the
// pair (preconditionOf); the routes that take no precondition never read either
// header and would accept it. So the CLI is the check that catches it.
func TestSignRejectsBothPreconditions(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "publisher.key")
	capture(t, func() error {
		return pubkeygen([]string{"-kid", "op-1", "-namespace", "ns", "-out", keyFile})
	})
	err := signCmd([]string{"-key", keyFile, "-kid", "op-1", "-path", "/admin/x",
		"-if-match", "b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c-live", "-create"})
	if err == nil {
		t.Fatal("signCmd accepted both -if-match and -create")
	}
}

// The wildcard allowlist is only mandatory once writes are possible: a
// read-only node must keep booting unrestricted, and opening the write plane
// without the constraint must fail loudly rather than quietly escalate.
func TestWritePlaneConfigBindsWildcardToPublisherKeys(t *testing.T) {
	dir := t.TempDir()
	gen := capture(t, func() error {
		return pubkeygen([]string{"-kid", "op-1", "-namespace", "beckn-testnet", "-out", filepath.Join(dir, "k")})
	})
	var entry string
	for _, line := range strings.Split(gen, "\n") {
		if strings.HasPrefix(line, "DEDI_PUBLISHER_KEYS=") {
			entry = strings.TrimPrefix(line, "DEDI_PUBLISHER_KEYS=")
		}
	}

	// Read-only node: no keys, no allowlist required, wildcard unrestricted.
	keys, wildcard, err := writePlaneConfig("", "")
	if err != nil {
		t.Fatalf("read-only node failed to boot: %v", err)
	}
	if keys.Len() != 0 || wildcard != nil {
		t.Fatalf("keys=%d wildcard=%v, want 0 and nil", keys.Len(), wildcard)
	}

	// Write plane open with no allowlist: must refuse to start.
	if _, _, err := writePlaneConfig(entry, ""); err == nil {
		t.Fatal("node started with publisher keys but no wildcard allowlist")
	}
	if _, _, err := writePlaneConfig(entry, "   ,  ,"); err == nil {
		t.Fatal("a whitespace-only allowlist was accepted as a constraint")
	}

	// Write plane open with an allowlist: fine, and entries are trimmed.
	keys, wildcard, err = writePlaneConfig(entry, " beckn-testnet , other-net ")
	if err != nil {
		t.Fatalf("valid write-plane config rejected: %v", err)
	}
	if keys.Len() != 1 {
		t.Fatalf("keys = %d", keys.Len())
	}
	if len(wildcard) != 2 || wildcard[0] != "beckn-testnet" || wildcard[1] != "other-net" {
		t.Fatalf("wildcard = %#v", wildcard)
	}

	if _, _, err := writePlaneConfig("garbage", "ns"); err == nil {
		t.Fatal("malformed publisher key spec accepted")
	}
}
