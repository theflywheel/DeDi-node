package api

import (
	"regexp"
	"strings"
	"testing"
)

// The console's security properties are documented in docs/operator-console.md
// and were, until now, asserted nowhere. They are the reason the page can be
// served publicly on the demo node without the demo being open to writes, so
// they are worth failing a build over.
//
// These read the embedded page rather than a running server: the properties are
// about what the bytes contain, and a page that never reaches a browser cannot
// leak anything.

// The pasted private key must be imported non-extractable. Extractable would
// mean any later script on the page — or anything that got itself injected —
// could read the operator's signing key back out of the CryptoKey object.
func TestPublisherKeyIsImportedNonExtractable(t *testing.T) {
	page := string(adminHTML)
	i := strings.Index(page, "importKey(")
	if i < 0 {
		t.Fatal("the console no longer imports a key; this test needs rewriting")
	}
	call := page[i:min(i+160, len(page))]
	// crypto.subtle.importKey(format, data, algorithm, extractable, usages)
	if !strings.Contains(call, "'Ed25519'}, false,") && !strings.Contains(call, `"Ed25519"}, false,`) {
		t.Errorf("the key is not imported with extractable=false:\n%s", call)
	}
}

// The key must live in one closure and reach no storage that outlives the tab.
//
// Matching on the bare word finds the comment that says the key is NOT kept in
// localStorage, which is how the first version of this test failed on correct
// code. It looks for actual use — a property access or an index — instead.
var storageUse = regexp.MustCompile(`\b(localStorage|sessionStorage|indexedDB)\s*[.\[]|document\.cookie\s*=`)

func TestPublisherKeyNeverReachesStorage(t *testing.T) {
	if m := storageUse.FindString(string(adminHTML)); m != "" {
		t.Errorf("the console uses %q; the signing key must not outlive the tab", m)
	}
}

// The textarea is cleared the moment the key is imported, so it does not
// survive in the DOM for a screenshot or a devtools scroll.
func TestPrivateKeyInputIsClearedOnImport(t *testing.T) {
	page := string(adminHTML)
	if !regexp.MustCompile(`\$\('k-priv'\)\.value\s*=\s*''`).MatchString(page) {
		t.Error("the private-key input is never cleared after import")
	}
}

// Writes are signed in the browser and sent with the signature; the key itself
// must never appear in a request body.
func TestSigningHappensInTheBrowser(t *testing.T) {
	page := string(adminHTML)
	for _, needed := range []string{"crypto.subtle.sign", "DeDi-Signature", "DeDi-Key-Id", "DeDi-Timestamp"} {
		if !strings.Contains(page, needed) {
			t.Errorf("the console no longer %s — writes would not be signed here", needed)
		}
	}
}

// Every write is conditional on the version the operator read, so a concurrent
// change fails loudly instead of being clobbered.
func TestWritesCarryTheirPrecondition(t *testing.T) {
	page := string(adminHTML)
	for _, needed := range []string{"If-Match", "ifMatch"} {
		if !strings.Contains(page, needed) {
			t.Errorf("the console does not send %s; a concurrent change would be silently overwritten", needed)
		}
	}
}

// The participants view reads with include_revoked, unlike the public plane. A
// console that hid revoked records would be hiding the outcome of its own most
// consequential action.
func TestConsoleShowsRevokedParticipants(t *testing.T) {
	if !strings.Contains(string(adminHTML), "include_revoked") {
		t.Error("the console does not request revoked records, so a revocation's outcome is invisible")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
