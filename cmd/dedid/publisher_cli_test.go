package main

// The CLI mints publisher keys and signs requests; the node verifies them. The
// two sides have to agree on key encoding and on the signing preimage, and
// nothing else in the tree checks that they do.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/publisher"
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

	const path = "/admin/records/x:publish"
	out := capture(t, func() error {
		return signCmd([]string{"-key", keyFile, "-kid", "op-1", "-method", "POST", "-path", path, "-body", bodyFile})
	})
	kid := header(t, out, publisher.HeaderKeyID)
	ts := header(t, out, publisher.HeaderTimestamp)
	sig := header(t, out, publisher.HeaderSignature)

	key, err := ks.Verify("POST", path, body, kid, ts, sig, time.Now(), publisher.DefaultMaxSkew)
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
	if _, err := ks.Verify("POST", path, append(body, ' '), kid, ts, sig, time.Now(), publisher.DefaultMaxSkew); err == nil {
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
