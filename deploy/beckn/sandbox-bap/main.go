// The searching application — the BAP side of the demo.
//
// It signs nothing, verifies nothing and resolves nothing: that is the ONIX
// adapter's job, and the whole point is that the adapter does it against dedid.
// This is the naive participant sitting behind its adapter. The BPP side is the
// real scheme-bpp from the beckn-demo repo, unmodified.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// received records what actually arrived, so the demo can assert on the
// message rather than on a log line.
type received struct {
	At     string          `json:"at"`
	Path   string          `json:"path"`
	Action string          `json:"action"`
	Body   json.RawMessage `json:"body"`
}

var (
	mu    sync.Mutex
	inbox []received
)

func main() {
	listen := flag.String("listen", ":3001", "listen address")
	caller := flag.String("caller", "", "the adapter caller base this app posts through")
	flag.Parse()

	mux := http.NewServeMux()

	// The BAP's adapter delivers verified callbacks here.
	mux.HandleFunc("POST /api/bap-webhook/{action}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Context struct {
				Action string `json:"action"`
			} `json:"context"`
		}
		json.Unmarshal(body, &env)
		mu.Lock()
		inbox = append(inbox, received{
			At: time.Now().UTC().Format(time.RFC3339Nano), Path: r.URL.Path,
			Action: env.Context.Action, Body: body,
		})
		mu.Unlock()
		log.Printf("<- %s action=%s (%d bytes)", r.URL.Path, env.Context.Action, len(body))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"ack":{"status":"ACK"}}}`))
	})

	// Start a search: the demo posts a discover here and this app sends it
	// through its adapter's caller, which signs it.
	mux.HandleFunc("POST /trigger", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		resp, err := http.Post(*caller+"/discover", "application/json", bytes.NewReader(body))
		out := map[string]any{}
		if err != nil {
			out["adapter_status"], out["adapter_body"] = 0, err.Error()
		} else {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			out["adapter_status"], out["adapter_body"] = resp.StatusCode, string(b)
		}
		log.Printf("-> discover  %v", out["adapter_status"])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(inbox)
	})
	mux.HandleFunc("POST /inbox/reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inbox = nil
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	log.Printf("bap app listening on %s (caller %s)", *listen, *caller)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
