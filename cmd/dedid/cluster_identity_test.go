package main

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/note"
)

// A clustered node with no configured key would self-provision one, and because
// self-provisioning is per-database each Raft replica would mint a different
// identity. The failure only shows up at the first leadership change, as the
// same origin signing under a new key — a forked log to any witness. Refusing to
// start is the only point at which this is cheap to notice.
func TestAClusteredNodeRefusesToSelfProvisionAnIdentity(t *testing.T) {
	inEmptyDir(t)
	t.Setenv("DEDI_KEY", "")
	t.Setenv("DEDI_KEY_FILE", "")
	t.Setenv("DEDI_CLUSTER_ID", "2")

	_, _, err := nodeKey(context.Background(), nil, "dedi.example/log")
	if err == nil {
		t.Fatal("a clustered node self-provisioned an identity")
	}
	// The message has to say what to do, not only that something is wrong:
	// whoever hits this is mid-deploy and has no key yet.
	for _, want := range []string{"DEDI_CLUSTER_ID", "DEDI_KEY", "keygen"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The requirement is a shared key, not a ceremony. A clustered node that has one
// starts normally.
func TestAClusteredNodeWithAConfiguredKeyStarts(t *testing.T) {
	inEmptyDir(t)
	skey, _, err := note.GenerateKey(rand.Reader, "dedi.example")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEDI_CLUSTER_ID", "2")
	t.Setenv("DEDI_KEY", skey)

	got, vkey, err := nodeKey(context.Background(), nil, "dedi.example/log")
	if err != nil {
		t.Fatalf("nodeKey: %v", err)
	}
	if got != skey || vkey == "" {
		t.Errorf("nodeKey returned %q/%q, want the configured key and its verifier", got, vkey)
	}
}

// A key file is the other explicit source and must count the same. This is how
// the compose cluster is actually wired.
func TestAClusteredNodeWithAKeyFileStarts(t *testing.T) {
	dir := inEmptyDir(t)
	skey, _, err := note.GenerateKey(rand.Reader, "dedi.example")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shared.key")
	if err := os.WriteFile(path, []byte(skey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEDI_CLUSTER_ID", "2")
	t.Setenv("DEDI_KEY", "")
	t.Setenv("DEDI_KEY_FILE", path)

	if got, _, err := nodeKey(context.Background(), nil, "dedi.example/log"); err != nil || got != skey {
		t.Fatalf("nodeKey = %q, %v; want the key file's key", got, err)
	}
}

// inEmptyDir runs the test somewhere with no dedid.key lying around, so the
// default-file source does not accidentally satisfy the check.
func inEmptyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	return dir
}
