// dedid is the DeDi node daemon: a tamper-evident public directory server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dedid <keygen|serve|seed> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
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
	var ttl int
	if _, err := fmt.Sscanf(envOr("DEDI_TTL", "300"), "%d", &ttl); err != nil {
		return fmt.Errorf("DEDI_TTL: %w", err)
	}

	cp := &checkpoint.Checkpointer{
		Store:    s,
		SKey:     strings.TrimSpace(string(skeyBytes)),
		Origin:   envOr("DEDI_ORIGIN", "dev.dedi.local/log"),
		Interval: interval,
	}
	go cp.Run(ctx)

	srv := &api.Server{Store: s, CP: cp, TTL: ttl}
	listen := envOr("DEDI_LISTEN", ":8080")
	log.Printf("dedid read plane listening on %s", listen)
	return http.ListenAndServe(listen, srv.Handler())
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
		} `json:"records"`
	} `json:"registries"`
}

// seed appends the contents of a seed file. Appends are unconditional: rerunning
// a seed produces new versions, which is harmless in dev.
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	file := fs.String("file", "", "seed JSON file")
	fs.Parse(args)
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
	if err := appendOne(store.AppendInput{EntryType: "namespace", Namespace: sf.Namespace, PayloadRaw: sf.Payload, CreatedBy: "seed"}); err != nil {
		return err
	}
	for _, reg := range sf.Registries {
		if err := appendOne(store.AppendInput{EntryType: "registry", Namespace: sf.Namespace, Registry: reg.Name, PayloadRaw: reg.Payload, CreatedBy: "seed"}); err != nil {
			return err
		}
		for _, rec := range reg.Records {
			if err := appendOne(store.AppendInput{EntryType: "record", Namespace: sf.Namespace, Registry: reg.Name, RecordName: rec.Name, PayloadRaw: rec.Payload, CreatedBy: "seed"}); err != nil {
				return err
			}
		}
	}
	return nil
}
