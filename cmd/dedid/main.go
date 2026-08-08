// dedid is the DeDi node daemon: a tamper-evident public directory server.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/anchor"
	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/witness"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dedid <keygen|pubkey|pubkeygen|sign|serve|seed> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "pubkey":
		err = pubkey(os.Args[2:])
	case "pubkeygen":
		err = pubkeygen(os.Args[2:])
	case "sign":
		err = signCmd(os.Args[2:])
	case "serve":
		err = serve()
	case "seed":
		err = seed(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "dedid.key", "private key output file")
	name := fs.String("name", "dedi.local", "key name (appears in checkpoint signatures)")
	fs.Parse(args)
	skey, vkey, err := note.GenerateKey(rand.Reader, *name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(skey+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("private key written to %s\npublic verifier key (distribute to clients):\n%s\n", *out, vkey)
	return nil
}

// pubkey recovers the verifier key for a node identity key.
//
// The verifier key is public, and it is the only thing a relying party or a
// witness needs in order to check this node's checkpoints — but keygen prints
// it once and an operator who did not keep it has, until now, no way back to it
// short of rotating the identity and invalidating every checkpoint already
// signed. It is derivable from the private key, so losing it should be an
// inconvenience rather than an incident.
func pubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	keyFile := fs.String("key", "", "node private key file (default: $DEDI_KEY, else dedid.key)")
	fs.Parse(args)

	skey := strings.TrimSpace(os.Getenv("DEDI_KEY"))
	if *keyFile != "" || skey == "" {
		path := *keyFile
		if path == "" {
			path = "dedid.key"
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read node key: %w", err)
		}
		skey = strings.TrimSpace(string(b))
	}

	vkey, err := verifierKeyFor(skey)
	if err != nil {
		return err
	}
	fmt.Println(vkey)
	return nil
}

// verifierKeyFor derives the public verifier key from a signed-note private
// key. The encoding is fixed by the note format: "PRIVATE+KEY", the key name,
// the key hash, and the base64 of an algorithm byte followed by the Ed25519
// seed. The hash is recomputed from the public key rather than copied out of
// the private key, so a corrupted input fails to verify later rather than
// producing a plausible-looking key that matches nothing.
func verifierKeyFor(skey string) (string, error) {
	// Split at most five ways: base64 uses '+' as a symbol, so the trailing
	// field has to be taken whole rather than split on its own contents.
	parts := strings.SplitN(skey, "+", 5)
	if len(parts) != 5 || parts[0] != "PRIVATE" || parts[1] != "KEY" {
		return "", fmt.Errorf("not a node private key: expected PRIVATE+KEY+<name>+<hash>+<base64>")
	}
	name, encoded := parts[2], parts[4]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("node private key is not valid base64: %w", err)
	}
	// One algorithm byte, then the seed.
	if len(raw) != 1+ed25519.SeedSize {
		return "", fmt.Errorf("node private key has unexpected length %d", len(raw))
	}
	pub := ed25519.NewKeyFromSeed(raw[1:]).Public().(ed25519.PublicKey)
	return note.NewEd25519VerifierKey(name, pub)
}

// withVerifier pairs a configured private key with its public verifier key.
//
// Both key paths have to yield a verifier key, not just the self-provisioning
// one. It is derivable from the private key — `dedid pubkey` has done exactly
// this for a while — and leaving it empty for a configured key had consequences
// well beyond the cosmetic:
//
//   - a node deployed from a key file could not enrol as a child at all. It
//     presented an empty key, the parent refused it as malformed, and the child
//     retried every fifteen seconds forever while every health surface on it
//     read fine.
//   - it published no verifier key of its own, so children it delegated were
//     handed a blank DEDI_PARENT_KEY and nobody could check its checkpoints
//     without asking it for the key out of band.
//
// Found by running a real chain of nodes rather than by reading this function,
// which looks entirely reasonable.
func withVerifier(skey string) (string, string, error) {
	vkey, err := verifierKeyFor(skey)
	if err != nil {
		return "", "", fmt.Errorf("derive verifier key from the configured node key: %w", err)
	}
	return skey, vkey, nil
}

// pubkeygen mints a publisher credential for the write plane. The private key
// stays with the operator; the printed entry is what the node is configured
// with, and it carries no secret.
func pubkeygen(args []string) error {
	fs := flag.NewFlagSet("pubkeygen", flag.ExitOnError)
	out := fs.String("out", "publisher.key", "private key output file")
	kid := fs.String("kid", "", "key id, recorded on every version this key publishes (required)")
	ns := fs.String("namespace", "", "namespace this key may write to (required)")
	fs.Parse(args)
	if *kid == "" || *ns == "" {
		return fmt.Errorf("pubkeygen: -kid and -namespace are required")
	}
	if strings.ContainsAny(*kid, ":, \t\n") || strings.ContainsAny(*ns, ":, \t\n") {
		return fmt.Errorf("pubkeygen: -kid and -namespace must not contain ':', ',' or whitespace")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	keyFile, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := keyFile.Write([]byte(base64.StdEncoding.EncodeToString(priv) + "\n")); err != nil {
		keyFile.Close()
		return err
	}
	if err := keyFile.Close(); err != nil {
		return err
	}
	fmt.Printf("private key written to %s (keep it secret)\n\nadd to the node's config:\nDEDI_PUBLISHER_KEYS=%s:%s:%s\n",
		*out, *kid, *ns, base64.StdEncoding.EncodeToString(pub))
	return nil
}

// signCmd prints the headers that authenticate one write request. Operator
// tooling and curl use this; the node only ever verifies.
func signCmd(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyFile := fs.String("key", "publisher.key", "publisher private key file")
	kid := fs.String("kid", "", "key id (required)")
	method := fs.String("method", "POST", "HTTP method")
	path := fs.String("path", "", "request URI, e.g. /admin/namespaces/beckn-testnet (required)")
	bodyFile := fs.String("body", "", "file containing the request body (empty for none)")
	ifMatch := fs.String("if-match", "", "version tag (<hex digest>-<state>) of the version being replaced")
	create := fs.Bool("create", false, "the target must not exist yet (If-None-Match: *)")
	curl := fs.Bool("curl", false, "print curl header flags instead of plain headers")
	fs.Parse(args)
	if *kid == "" || *path == "" {
		return fmt.Errorf("sign: -kid and -path are required")
	}
	// The precondition is signed, so it has to be decided here rather than
	// added to the request afterwards — see publisher.Preimage.
	if (*ifMatch == "") == !*create {
		return fmt.Errorf("sign: give exactly one of -if-match <digest>-<state> or -create")
	}
	pre := publisher.Precondition{IfMatch: *ifMatch}
	if *create {
		pre.IfNoneMatch = "*"
	}
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		return fmt.Errorf("read publisher key: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("publisher key must be a base64 Ed25519 private key")
	}
	var body []byte
	if *bodyFile != "" {
		if body, err = os.ReadFile(*bodyFile); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	sig := publisher.Sign(ed25519.PrivateKey(priv), *method, *path, body, pre, now)
	hdrs := [][2]string{
		{publisher.HeaderKeyID, *kid},
		{publisher.HeaderTimestamp, now.Format(time.RFC3339)},
		{publisher.HeaderSignature, sig},
	}
	if pre.IfMatch != "" {
		hdrs = append(hdrs, [2]string{"If-Match", pre.IfMatch})
	}
	if pre.IfNoneMatch != "" {
		hdrs = append(hdrs, [2]string{"If-None-Match", pre.IfNoneMatch})
	}
	for _, h := range hdrs {
		if *curl {
			fmt.Printf("-H '%s: %s' ", h[0], h[1])
		} else {
			fmt.Printf("%s: %s\n", h[0], h[1])
		}
	}
	if *curl {
		fmt.Println()
	}
	return nil
}

// writePlaneConfig resolves the publisher keys and the Beckn wildcard
// allowlist together, because the eligibility constraint only binds once
// writes are possible.
//
// A node with no publisher keys is read-only, and an unrestricted wildcard is
// safe there — that is today's reference deployment, and it keeps working
// untouched. The moment a key is configured, the allowlist becomes mandatory:
// without it any publisher could answer for any subscriber_id on the node,
// which design.md:256 forbids the publisher plane from shipping.
func writePlaneConfig(keysSpec, wildcardSpec string) (*publisher.KeySet, []string, error) {
	keys, err := publisher.ParseKeySet(keysSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("DEDI_PUBLISHER_KEYS: %w", err)
	}
	var wildcard []string
	for _, ns := range strings.Split(wildcardSpec, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			wildcard = append(wildcard, ns)
		}
	}
	if keys.Len() > 0 && wildcard == nil {
		return nil, nil, fmt.Errorf("DEDI_WILDCARD_NAMESPACES must list the namespaces eligible " +
			"for Beckn wildcard lookup when DEDI_PUBLISHER_KEYS is set: without it any publisher " +
			"key could answer for any subscriber_id on the node (design.md:256)")
	}
	return keys, wildcard, nil
}

func openStore(ctx context.Context) (*store.Store, error) {
	// DATABASE_URL is what managed Postgres add-ons inject; DEDI_DB_URL still
	// wins when both are present, so pointing the node at a different database
	// than the platform's default needs no unsetting.
	dbURL := envOr("DEDI_DB_URL", envOr("DATABASE_URL",
		"postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable"))
	s, err := store.Open(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect database %s: %s", redactDatabaseURL(dbURL), redactDatabaseError(err, dbURL))
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// nodeKey resolves the identity key that signs this node's checkpoints, and
// returns it with its verifier key when that is known.
//
// Three sources, in descending order of how explicitly the operator asked for
// them: DEDI_KEY, a key file, and failing both, the node's own database. The
// last one is what makes a node deployable in one click — a platform that hands
// you a Postgres and nothing else is the common case, and demanding a key
// minted on a laptop first turns every "deploy this" into "deploy this, but
// first install Go". The key is generated once and kept, so the node keeps the
// identity its existing checkpoints were signed under.
//
// The verifier key comes back empty for the explicit sources: a note private
// key does not carry its public half in a form we can recover without
// reimplementing the note format, and operators supplying their own key already
// have the verifier key that keygen printed alongside it.
func nodeKey(ctx context.Context, s *store.Store, origin string) (skey, vkey string, err error) {
	if k := strings.TrimSpace(os.Getenv("DEDI_KEY")); k != "" {
		return withVerifier(k)
	}
	// An explicitly configured file that cannot be read is a misconfiguration,
	// not an invitation to mint a new identity: silently signing under a
	// different key would look like a forked history to every witness watching.
	explicit := os.Getenv("DEDI_KEY_FILE")
	keyFile := explicit
	if keyFile == "" {
		keyFile = "dedid.key"
	}
	b, readErr := os.ReadFile(keyFile)
	switch {
	case readErr == nil:
		return withVerifier(strings.TrimSpace(string(b)))
	case explicit != "":
		return "", "", fmt.Errorf("read node key from DEDI_KEY_FILE: %w", readErr)
	case !errors.Is(readErr, fs.ErrNotExist):
		return "", "", fmt.Errorf("read node key %s: %w", keyFile, readErr)
	}

	// The generated key is only a candidate — if this node already has an
	// identity, or another replica claims one first, EnsureIdentity returns the
	// stored one and this key is discarded.
	candidateSKey, candidateVKey, err := note.GenerateKey(rand.Reader, noteName(origin))
	if err != nil {
		return "", "", fmt.Errorf("generate node key: %w", err)
	}
	skey, vkey, err = s.EnsureIdentity(ctx, candidateSKey, candidateVKey)
	if err != nil {
		return "", "", fmt.Errorf("claim node identity: %w", err)
	}
	if skey == candidateSKey {
		log.Printf("generated node identity for origin %s", origin)
	}
	// Printed on every boot, not just the first: this is the key third parties
	// need to verify this node's checkpoints, and an operator who did not
	// capture it at creation has no other way to recover it.
	log.Printf("node verifier key (distribute to clients and witnesses):\n%s", vkey)
	return skey, vkey, nil
}

// noteName adapts an origin into a signed-note key name. The name appears in
// every checkpoint signature, so tying it to the origin keeps the signature
// self-describing; note names may not contain '+' or whitespace, which an
// origin is under no obligation to respect.
func noteName(origin string) string {
	name := strings.Map(func(r rune) rune {
		if r == '+' || unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, origin)
	if name == "" {
		return "dedi.local"
	}
	return name
}

func redactDatabaseError(err error, dbURL string) string {
	msg := err.Error()
	redacted := redactDatabaseURL(dbURL)
	if redacted != dbURL {
		msg = strings.ReplaceAll(msg, dbURL, redacted)
	}
	return msg
}

func redactDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.UserPassword("redacted", "redacted")
	return u.String()
}

// openCluster starts Raft replication if DEDI_CLUSTER_ID is set, and returns
// nil otherwise — an unclustered node keeps behaving exactly as before, which
// is what every existing deployment expects.
func openCluster(s *store.Store) (*cluster.Node, error) {
	id := os.Getenv("DEDI_CLUSTER_ID")
	if id == "" {
		return nil, nil
	}
	peers, err := cluster.ParsePeers(os.Getenv("DEDI_CLUSTER_PEERS"))
	if err != nil {
		return nil, fmt.Errorf("DEDI_CLUSTER_PEERS: %w", err)
	}
	if len(peers) == 0 {
		return nil, fmt.Errorf("DEDI_CLUSTER_ID is set but DEDI_CLUSTER_PEERS is empty")
	}
	if len(peers)%2 == 0 {
		// An even cluster tolerates no more failures than the odd one below it
		// and has more ways to lose quorum. Worth saying out loud rather than
		// silently accepting a configuration that costs a machine for nothing.
		log.Printf("cluster: %d members is an even number — %d members would tolerate the same "+
			"single failure with one machine fewer", len(peers), len(peers)-1)
	}
	node, err := cluster.Open(cluster.Config{
		ID:        id,
		BindAddr:  os.Getenv("DEDI_CLUSTER_BIND"),
		DataDir:   envOr("DEDI_CLUSTER_DATA_DIR", "/data/raft"),
		Peers:     peers,
		Bootstrap: os.Getenv("DEDI_CLUSTER_BOOTSTRAP") == "true",
		LogOutput: log.Writer(),
		OnHalt: func(err error) {
			// A replica that cannot apply a committed entry has no safe way to
			// carry on: continuing means serving a tree that differs from the
			// rest of the cluster while looking perfectly healthy. Dying is
			// visible, and the supervisor will restart it to try again.
			log.Fatalf("cluster: halting this replica rather than diverging: %v", err)
		},
	}, s)
	if err != nil {
		return nil, err
	}
	log.Printf("cluster %q: %d members, bootstrap=%v", id, len(peers),
		os.Getenv("DEDI_CLUSTER_BOOTSTRAP") == "true")
	return node, nil
}

func serve() error {
	ctx := context.Background()
	s, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	origin := envOr("DEDI_ORIGIN", "dev.dedi.local/log")
	skey, vkey, err := nodeKey(ctx, s, origin)
	if err != nil {
		return err
	}
	interval, err := time.ParseDuration(envOr("DEDI_CHECKPOINT_INTERVAL", "30s"))
	if err != nil {
		return fmt.Errorf("DEDI_CHECKPOINT_INTERVAL: %w", err)
	}
	if interval <= 0 {
		return fmt.Errorf("DEDI_CHECKPOINT_INTERVAL must be positive")
	}
	ttl, err := strconv.Atoi(envOr("DEDI_TTL", "300"))
	if err != nil {
		return fmt.Errorf("DEDI_TTL: %w", err)
	}

	// Optional: replicate the log across a cluster (high availability). Raft
	// gives every replica the same ordered commands so they compute the same
	// tree; it is crash tolerance, not trust — a quorum run by one operator
	// agrees with that operator. Tamper evidence stays with the witness ring.
	clu, err := openCluster(s)
	if err != nil {
		return err
	}
	if clu != nil {
		defer clu.Close()
	}

	cp := &checkpoint.Checkpointer{
		Store:    s,
		SKey:     skey,
		Origin:   origin,
		Interval: interval,
	}
	if clu != nil {
		// Only the leader signs, and the signed note is replicated rather than
		// each replica re-signing its own view — two replicas at slightly
		// different sizes would otherwise produce two roots under one key.
		cp.Publish = clu.SignCheckpoint
		cp.IsWriter = clu.IsLeader
	}
	go cp.Run(ctx)

	// Optional: witness another node's log (decentralised trust). When
	// DEDI_WITNESS_TARGET_URL is set, this node periodically verifies the
	// target is append-only and records each verdict under `_witness`.
	witnessTargetURL := os.Getenv("DEDI_WITNESS_TARGET_URL")
	witnessTargetOrigin := envOr("DEDI_WITNESS_TARGET_ORIGIN", "target")
	var wit *witness.Witness
	if wt := witnessTargetURL; wt != "" {
		wiv, err := time.ParseDuration(envOr("DEDI_WITNESS_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("DEDI_WITNESS_INTERVAL: %w", err)
		}
		wit = &witness.Witness{
			Store:     s,
			TargetURL: wt,
			TargetKey: os.Getenv("DEDI_WITNESS_TARGET_KEY"),
			Origin:    witnessTargetOrigin,
			Interval:  wiv,
		}
		if clu != nil {
			// Verdicts are log entries, so they go through the leader like any
			// other write; followers stand by rather than each polling the same
			// target to learn the same fact.
			wit.Writer = clu
			wit.IsWriter = clu.IsLeader
		}
		go wit.Run(ctx)
		log.Printf("witnessing %s every %s", wt, wiv)
	}

	// Optional: anchor signed checkpoints to an external ledger (adapter-based;
	// secondary to witnessing). DEDI_ANCHOR_BACKEND selects the adapter.
	if backend := os.Getenv("DEDI_ANCHOR_BACKEND"); backend != "" {
		aiv, err := time.ParseDuration(envOr("DEDI_ANCHOR_INTERVAL", "5m"))
		if err != nil {
			return fmt.Errorf("DEDI_ANCHOR_INTERVAL: %w", err)
		}
		var ledger anchor.Ledger
		switch backend {
		case "cord":
			prefix, err := strconv.Atoi(envOr("DEDI_ANCHOR_SS58", "29"))
			if err != nil {
				return fmt.Errorf("DEDI_ANCHOR_SS58: %w", err)
			}
			ledger = &anchor.CORD{
				RPCURL:     envOr("DEDI_ANCHOR_RPC_URL", "ws://127.0.0.1:9944"),
				SURI:       os.Getenv("DEDI_ANCHOR_SURI"),
				SS58Prefix: uint16(prefix),
			}
		default:
			return fmt.Errorf("DEDI_ANCHOR_BACKEND: unknown backend %q (supported: cord)", backend)
		}
		go (&anchor.Anchorer{Store: s, Ledger: ledger, Interval: aiv}).Run(ctx)
		log.Printf("anchoring checkpoints to %s every %s", backend, aiv)
	}

	keys, wildcard, err := writePlaneConfig(os.Getenv("DEDI_PUBLISHER_KEYS"), os.Getenv("DEDI_WILDCARD_NAMESPACES"))
	if err != nil {
		return err
	}
	if keys.Len() > 0 {
		log.Printf("write plane open: %d publisher key(s); wildcard namespaces: %s",
			keys.Len(), strings.Join(wildcard, ", "))
	}
	statsInterval, err := time.ParseDuration(envOr("DEDI_STATS_FLUSH_INTERVAL", "10s"))
	if err != nil {
		return fmt.Errorf("DEDI_STATS_FLUSH_INTERVAL: %w", err)
	}
	if statsInterval <= 0 {
		return fmt.Errorf("DEDI_STATS_FLUSH_INTERVAL must be positive")
	}

	// A node that minted its own key already knows its verifier key, so the
	// explorer can show it without the operator copying it back in by hand.
	// An explicit DEDI_VERIFIER_KEY still wins, since only the operator knows
	// the public half of a key they supplied themselves.
	// The other nodes carrying this network. Observing them is a different and
	// weaker thing than witnessing one of them: this only establishes that a
	// peer answered, which is what a network overview should claim and no more.
	peers, err := network.ParsePeers(os.Getenv("DEDI_PEERS"))
	if err != nil {
		return fmt.Errorf("DEDI_PEERS: %w", err)
	}
	peerInterval, err := time.ParseDuration(envOr("DEDI_PEER_INTERVAL", "30s"))
	if err != nil {
		return fmt.Errorf("DEDI_PEER_INTERVAL: %w", err)
	}
	// Always created, even with no configured peers: delegating a child adds
	// one while the node is running, and a nil monitor would have nowhere to
	// put it until the next restart.
	netmon := &network.Monitor{Peers: peers, Interval: peerInterval}
	go netmon.Run(ctx)
	if len(peers) > 0 {
		names := make([]string, 0, len(peers))
		for _, p := range peers {
			names = append(names, p.Name)
		}
		log.Printf("network of %d nodes; watching %s every %s",
			len(peers)+1, strings.Join(names, ", "), peerInterval)
	}

	// The URL a child is told to call back on. Behind a proxy the request Host
	// is the proxy's, so this cannot be inferred per-request.
	publicURL := strings.TrimRight(os.Getenv("DEDI_PUBLIC_URL"), "/")

	// Children this node has delegated namespaces to: witnessed continuously,
	// and resumed across restarts.
	childSup, err := newChildSupervisor(s, clu, netmon)
	if err != nil {
		return fmt.Errorf("DEDI_CHILD_WITNESS_INTERVAL: %w", err)
	}
	childSup.Resume(ctx, keys.Namespaces())

	// If this node *is* a child, claim its offer.
	// The writer, so the namespace it creates takes the leader path on a
	// clustered child exactly like any other write.
	var childWriter interface {
		Append(context.Context, store.AppendInput) (store.Entry, error)
	} = s
	if clu != nil {
		childWriter = clu
	}
	enrolIfChild(ctx, childWriter, s, origin, envOr("DEDI_VERIFIER_KEY", vkey))

	srv := &api.Server{Store: s, CP: cp, TTL: ttl, VerifierKey: envOr("DEDI_VERIFIER_KEY", vkey),
		NodeName: os.Getenv("DEDI_NODE_NAME"), Network: netmon,
		WitnessTarget: witnessTargetOrigin, WitnessTargetURL: witnessTargetURL,
		WitnessTargetKey: os.Getenv("DEDI_WITNESS_TARGET_KEY"),
		DemoURL:          os.Getenv("DEDI_DEMO_URL"), WildcardNamespaces: wildcard,
		PublicURL:    publicURL,
		OnDelegation: func(rec delegation.Record) { childSup.Apply(ctx, rec) }}
	if clu != nil {
		srv.Writer = clu
		srv.Cluster = clu.State
	}
	if wit != nil {
		// Adapted rather than passed through, so the api package stays free of a
		// dependency on witness: witness's own tests import api, and the cycle
		// would not build.
		srv.WitnessHealth = func() api.WitnessState { return witnessState(wit.Status()) }
	}
	// The same adaptation per child, so the Children tab can say whether the
	// verdict it is showing is still being refreshed. Without it a stalled
	// child-witness is invisible: the verdict stays put and keeps reading
	// consistency_ok.
	srv.ChildWitnessHealth = func(origin string) (api.WitnessState, bool) {
		h, running := childSup.Health(origin)
		return witnessState(h), running
	}
	if keys.Len() > 0 {
		srv.Auth = &publisher.Authenticator{Keys: keys}
	}
	handler := srv.Handler()
	// Requests are counted in memory and folded into the store on this cadence,
	// so the served-request total survives restarts and sums across replicas.
	go srv.RunCounterFlush(ctx, statsInterval)

	// PaaS platforms assign the port at runtime via $PORT; an explicit
	// DEDI_LISTEN still wins so local and compose setups are unaffected.
	listen := os.Getenv("DEDI_LISTEN")
	if listen == "" {
		if port := os.Getenv("PORT"); port != "" {
			listen = ":" + port
		} else {
			listen = ":8080"
		}
	}
	log.Printf("dedid read plane listening on %s", listen)
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server.ListenAndServe()
}

type seedFile struct {
	Namespace  string          `json:"namespace"`
	Payload    json.RawMessage `json:"payload"`
	Registries []struct {
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
		Records []struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
			// Optional explicit state: "live" (default) or "revoked" — the
			// operator's revocation path (a revocation is a new version).
			State string `json:"state"`
		} `json:"records"`
	} `json:"registries"`
}

// seed appends the contents of a seed file. Appends are unconditional: rerunning
// a seed produces new versions, which is harmless in dev.
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	file := fs.String("file", "", "seed JSON file")
	// created_by is inside the Merkle leaf, so it is covered by inclusion
	// proofs. Naming the actual operator makes the log's audit trail useful;
	// the default keeps existing invocations working.
	by := fs.String("by", "seed", "author recorded on every entry (appears in the log and in proofs)")
	fs.Parse(args)
	if strings.TrimSpace(*by) == "" {
		return fmt.Errorf("seed: -by must not be empty")
	}
	if *file == "" {
		return fmt.Errorf("seed: -file is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var sf seedFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return err
	}
	ctx := context.Background()
	s, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	appendOne := func(in store.AppendInput) error {
		e, err := s.Append(ctx, in)
		if err != nil {
			return fmt.Errorf("append %s %s/%s/%s: %w", in.EntryType, in.Namespace, in.Registry, in.RecordName, err)
		}
		log.Printf("seq=%d %s %s/%s/%s v%d", e.Seq, e.EntryType, e.Namespace, e.Registry, e.RecordName, e.VersionNum)
		return nil
	}
	if err := appendOne(store.AppendInput{EntryType: "namespace", Namespace: sf.Namespace, PayloadRaw: sf.Payload, CreatedBy: *by}); err != nil {
		return err
	}
	for _, reg := range sf.Registries {
		if err := appendOne(store.AppendInput{EntryType: "registry", Namespace: sf.Namespace, Registry: reg.Name, PayloadRaw: reg.Payload, CreatedBy: *by}); err != nil {
			return err
		}
		for _, rec := range reg.Records {
			if err := appendOne(store.AppendInput{EntryType: "record", Namespace: sf.Namespace, Registry: reg.Name, RecordName: rec.Name, PayloadRaw: rec.Payload, State: rec.State, CreatedBy: *by}); err != nil {
				return err
			}
		}
	}
	return nil
}

// witnessState adapts a witness health snapshot for the api package, which
// cannot import witness — witness's own tests import api, and the cycle would
// not build.
func witnessState(h witness.Health) api.WitnessState {
	return api.WitnessState{
		LastAttemptAt: h.LastAttemptAt, LastSuccessAt: h.LastSuccessAt,
		LastError: h.LastError, Attempts: h.Attempts, Failures: h.Failures,
		Interval: h.Interval, Standby: h.Standby,
	}
}
