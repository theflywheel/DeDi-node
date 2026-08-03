// dedid is the DeDi node daemon: a tamper-evident public directory server.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/anchor"
	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/witness"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dedid <keygen|pubkeygen|sign|serve|seed> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
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
	path := fs.String("path", "", "request URI, e.g. /admin/namespaces/beckn-testnet?expected_version=0 (required)")
	bodyFile := fs.String("body", "", "file containing the request body (empty for none)")
	curl := fs.Bool("curl", false, "print curl header flags instead of plain headers")
	fs.Parse(args)
	if *kid == "" || *path == "" {
		return fmt.Errorf("sign: -kid and -path are required")
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
	sig := publisher.Sign(ed25519.PrivateKey(priv), *method, *path, body, now)
	hdrs := [][2]string{
		{publisher.HeaderKeyID, *kid},
		{publisher.HeaderTimestamp, now.Format(time.RFC3339)},
		{publisher.HeaderSignature, sig},
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
	dbURL := envOr("DEDI_DB_URL", "postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable")
	s, err := store.Open(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", dbURL, err)
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func serve() error {
	ctx := context.Background()
	s, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	keyFile := envOr("DEDI_KEY_FILE", "dedid.key")
	skeyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read node key (run 'dedid keygen' first): %w", err)
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

	cp := &checkpoint.Checkpointer{
		Store:    s,
		SKey:     strings.TrimSpace(string(skeyBytes)),
		Origin:   envOr("DEDI_ORIGIN", "dev.dedi.local/log"),
		Interval: interval,
	}
	go cp.Run(ctx)

	// Optional: witness another node's log (decentralised trust). When
	// DEDI_WITNESS_TARGET_URL is set, this node periodically verifies the
	// target is append-only and records each verdict under `_witness`.
	if wt := os.Getenv("DEDI_WITNESS_TARGET_URL"); wt != "" {
		wiv, err := time.ParseDuration(envOr("DEDI_WITNESS_INTERVAL", "60s"))
		if err != nil {
			return fmt.Errorf("DEDI_WITNESS_INTERVAL: %w", err)
		}
		go (&witness.Witness{
			Store:     s,
			TargetURL: wt,
			TargetKey: os.Getenv("DEDI_WITNESS_TARGET_KEY"),
			Origin:    envOr("DEDI_WITNESS_TARGET_ORIGIN", "target"),
			Interval:  wiv,
		}).Run(ctx)
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

	srv := &api.Server{Store: s, CP: cp, TTL: ttl, VerifierKey: os.Getenv("DEDI_VERIFIER_KEY"),
		WildcardNamespaces: wildcard}
	if keys.Len() > 0 {
		srv.Auth = &publisher.Authenticator{Keys: keys}
	}
	listen := envOr("DEDI_LISTEN", ":8080")
	log.Printf("dedid read plane listening on %s", listen)
	server := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
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
