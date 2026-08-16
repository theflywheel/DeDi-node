// Package crawl is the other half of the DeDi standard: reading the files
// other publishers produce, rather than producing our own.
//
// docs/spec/lfdt/docs/publishing-dedi-files.md §1.3 makes "DeDi server" a role
// distinct from "publisher", and §13 gives it four conditions. We already met
// the last one (honouring freshness and state) by having nothing to ignore.
// This package is what lets us meet the other three: verify every ingested file
// end to end and reject unauthenticated data, serve records and signatures
// unaltered, and expose each one at its {namespace}/{registry}/{record} triple.
//
// The split in this file is deliberate: Fetch does the network and all of the
// verification and returns a value; nothing here writes to the log. Ingest
// (ingest.go) takes that value and decides what to store. Keeping them apart is
// what makes the verification testable without a database and the storage
// testable without a network.
package crawl

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/webhook"
)

// maxDocument caps how much we will read from a remote publisher. A crawler is
// a "make this server fetch a URL you chose" capability, so an unbounded read
// is a memory-exhaustion primitive handed to anyone we crawl.
const maxDocument = 8 << 20 // 8 MiB

// ErrKeyChanged means the domain is now signing with a key we have not seen
// before, and we hold a pin for it.
//
// This is the case §14 leaves open ("an optional witness that logs manifest
// key-changes") and the reason crawling cannot be a pure fetch. A silent key
// rotation is indistinguishable from a takeover, so it stops the crawl and asks
// for a human rather than quietly ingesting whatever the new key vouches for.
var ErrKeyChanged = errors.New("manifest signing key changed since the last crawl")

// ErrStale means the document's own next_update is in the past. §9 makes
// freshness the publisher's promise about its own re-issue cadence, and §13
// requires a server to honour it, so a stale document is refused rather than
// ingested with a shrug.
var ErrStale = errors.New("document is past its next_update")

// Result is one successful crawl of one domain: the manifest, and every file it
// listed, all verified.
type Result struct {
	Domain   string
	Manifest dedifile.Manifest
	Files    []dedifile.File
	// KeyID and Key are the manifest key the crawl verified against, for
	// pinning. A publisher may list several; this is the one that checked out.
	KeyID string
	Key   ed25519.PublicKey
}

// Fetcher performs verified crawls.
type Fetcher struct {
	// Client is the HTTP client. nil gets one with a sane timeout.
	Client *http.Client
	// Now defaults to time.Now; injectable so freshness has a test.
	Now func() time.Time
	// AllowPrivateTargets permits crawling a host that is not publicly
	// routable. Off by default for the reason webhook delivery has the same
	// switch: the node makes this request from inside the operator's network,
	// so an unchecked target is a request-forgery primitive, and on every major
	// cloud the link-local metadata address hands out instance credentials.
	AllowPrivateTargets bool
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Fetch crawls one domain and returns everything it publishes, or an error.
//
// It is all-or-nothing on purpose. A partial crawl — manifest accepted, one
// file failed — would leave the caller holding a directory that is internally
// inconsistent with the manifest that vouched for it, and §7.3 verifies the two
// together. Refusing the whole crawl means a publisher's bad file cannot
// silently take out only part of their directory in our copy.
//
// pinnedKey, when non-empty, is the base64url x value of the key an earlier
// crawl of this domain verified against. See ErrKeyChanged.
func (f *Fetcher) Fetch(ctx context.Context, domain, pinnedKey string) (Result, error) {
	base, host, err := f.originFor(ctx, domain)
	if err != nil {
		return Result{}, err
	}

	var manifest dedifile.Manifest
	manifestURL := base + "/.well-known/dedi.index.json"
	if err := f.getJSON(ctx, manifestURL, &manifest); err != nil {
		return Result{}, fmt.Errorf("fetching manifest: %w", err)
	}

	key, kid, err := f.verifyManifest(manifest, host, pinnedKey)
	if err != nil {
		return Result{}, err
	}
	if err := f.checkFresh(manifest.NextUpdate, "manifest"); err != nil {
		return Result{}, err
	}

	files := make([]dedifile.File, 0, len(manifest.Files))
	seen := map[string]bool{}
	for _, entry := range manifest.Files {
		if seen[entry.Registry] {
			return Result{}, fmt.Errorf("manifest lists registry %q twice", entry.Registry)
		}
		seen[entry.Registry] = true
		file, err := f.fetchFile(ctx, base, entry, manifest.Domain)
		if err != nil {
			return Result{}, fmt.Errorf("file %q: %w", entry.Registry, err)
		}
		files = append(files, file)
	}
	return Result{Domain: host, Manifest: manifest, Files: files, KeyID: kid, Key: key}, nil
}

// verifyManifest checks the manifest's own signature and, if we hold a pin,
// that it is still the key we pinned.
//
// A manifest is self-signed: it carries the keys it is signed with, so the
// signature proves internal consistency, not authority. Authority comes from
// having fetched it over TLS from the domain it names, plus continuity with
// what that domain signed with last time — which is why the pin, not the
// signature, is what actually stops a takeover.
func (f *Fetcher) verifyManifest(m dedifile.Manifest, domain, pinnedKey string) (ed25519.PublicKey, string, error) {
	if len(m.Keys) == 0 {
		return nil, "", errors.New("manifest declares no keys")
	}
	if got := normalizeDomain(m.Domain); got != normalizeDomain(domain) {
		// A manifest served at example.org that claims to speak for
		// other.example is either misconfigured or is trying to have us file
		// its records under someone else's name.
		return nil, "", fmt.Errorf("manifest at %s claims domain %q", domain, m.Domain)
	}
	var lastErr error
	for _, jwk := range m.Keys {
		pub, err := dedifile.PublicKeyFromJWK(jwk)
		if err != nil {
			lastErr = err
			continue
		}
		if err := dedifile.VerifyManifest(m, pub); err != nil {
			lastErr = err
			continue
		}
		if pinnedKey != "" && jwk.X != pinnedKey {
			// Verified, but not by the key we trusted last time. Loud, and
			// specifically not "signature invalid" — the operator needs to know
			// the difference between a forgery and a rotation.
			return nil, "", fmt.Errorf("%w: pinned %s, now signing with %s", ErrKeyChanged, pinnedKey, jwk.X)
		}
		return pub, jwk.Kid, nil
	}
	return nil, "", fmt.Errorf("no manifest key verifies its signature: %w", lastErr)
}

// fetchFile retrieves one file, checks the manifest's digest commitment over
// the exact bytes served, and verifies the file's own signature.
func (f *Fetcher) fetchFile(ctx context.Context, base string, entry dedifile.ManifestEntry, manifestDomain string) (dedifile.File, error) {
	if err := f.sameOrigin(base, entry.URL); err != nil {
		return dedifile.File{}, err
	}
	raw, err := f.get(ctx, entry.URL)
	if err != nil {
		return dedifile.File{}, err
	}
	// Digest first, over the bytes as served. §6.3 makes this how a signed
	// manifest vouches for a file, and §7.3 step 3 is where it is checked; doing
	// it before parsing means a file that fails never gets interpreted at all.
	sum := sha256.Sum256(raw)
	if want, got := entry.Digest, "sha-256:"+hex.EncodeToString(sum[:]); want != got {
		return dedifile.File{}, fmt.Errorf("digest mismatch: manifest committed %s, served bytes are %s", want, got)
	}
	var file dedifile.File
	if err := json.Unmarshal(raw, &file); err != nil {
		return dedifile.File{}, fmt.Errorf("parsing: %w", err)
	}
	if normalizeDomain(file.Publisher.Domain) != normalizeDomain(manifestDomain) {
		return dedifile.File{}, fmt.Errorf("publisher.domain %q does not match the manifest's %q",
			file.Publisher.Domain, manifestDomain)
	}
	pub, err := dedifile.PublicKeyFromJWK(file.Publisher.Key)
	if err != nil {
		return dedifile.File{}, fmt.Errorf("publisher key: %w", err)
	}
	if err := dedifile.VerifyFile(file, pub); err != nil {
		return dedifile.File{}, fmt.Errorf("signature: %w", err)
	}
	if err := f.checkFresh(file.NextUpdate, "file"); err != nil {
		return dedifile.File{}, err
	}
	// §5.1: record_name is unique within a file, and a duplicate is grounds for
	// rejecting the whole file rather than for picking a winner.
	names := map[string]bool{}
	for _, r := range file.Records {
		if names[r.RecordName] {
			return dedifile.File{}, fmt.Errorf("duplicate record_name %q", r.RecordName)
		}
		names[r.RecordName] = true
	}
	return file, nil
}

func (f *Fetcher) checkFresh(nextUpdate, what string) error {
	if nextUpdate == "" {
		return fmt.Errorf("%s declares no next_update", what)
	}
	t, err := time.Parse(time.RFC3339, nextUpdate)
	if err != nil {
		return fmt.Errorf("%s next_update %q is not RFC 3339: %w", what, nextUpdate, err)
	}
	if t.Before(f.now()) {
		return fmt.Errorf("%w: %s next_update was %s", ErrStale, what, nextUpdate)
	}
	return nil
}

// sameOrigin refuses a manifest that points its files at another host.
//
// Without this, any domain we crawl could list a file URL on a domain it does
// not control and have us attribute that content to itself — or point us at an
// internal address and use our crawler as a probe.
func (f *Fetcher) sameOrigin(base, fileURL string) error {
	b, err := url.Parse(base)
	if err != nil {
		return err
	}
	u, err := url.Parse(fileURL)
	if err != nil {
		return fmt.Errorf("file url %q: %w", fileURL, err)
	}
	if u.Scheme != b.Scheme || !strings.EqualFold(u.Host, b.Host) {
		return fmt.Errorf("file url %q is not on the crawled origin %s", fileURL, base)
	}
	return nil
}

// originFor turns a domain into the origin to crawl and the bare host that
// origin speaks for, refusing hosts that are not publicly routable unless the
// operator opted in.
//
// The host is returned separately because it, not the caller's input string, is
// what the manifest must claim: a caller may hand us "https://example.org/" and
// a manifest legitimately says "example.org".
func (f *Fetcher) originFor(ctx context.Context, domain string) (base, hostPort string, err error) {
	d := normalizeDomain(domain)
	if d == "" {
		return "", "", errors.New("domain is required")
	}
	scheme := "https"
	if h, ok := strings.CutPrefix(d, "http://"); ok {
		scheme, d = "http", h
	} else if h, ok := strings.CutPrefix(d, "https://"); ok {
		d = h
	}
	d = strings.TrimSuffix(d, "/")
	if f.AllowPrivateTargets {
		return scheme + "://" + d, d, nil
	}
	host := d
	if h, _, err := net.SplitHostPort(d); err == nil {
		host = h
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", "", fmt.Errorf("domain %q does not resolve: %w", host, err)
	}
	for _, a := range addrs {
		if !webhook.PublicIP(a.IP) {
			return "", "", fmt.Errorf("domain %q resolves to %s, which is not public; refusing to crawl", host, a.IP)
		}
	}
	return scheme + "://" + d, d, nil
}

func (f *Fetcher) getJSON(ctx context.Context, u string, v any) error {
	raw, err := f.get(ctx, u)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (f *Fetcher) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDocument {
		return nil, fmt.Errorf("GET %s: document exceeds %d bytes", u, maxDocument)
	}
	return raw, nil
}

func normalizeDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}
