// scheme-bpp is a minimal Beckn BPP application that makes India's myScheme
// dataset searchable over a Beckn network. It plugs in where the starter kit's
// sandbox-bpp sits: the ONIX adapter forwards an inbound action to /api/webhook,
// and this app answers a `search` by posting an `on_search` catalog of matching
// schemes back to the adapter's caller endpoint (which signs + routes it).
//
// Pure stdlib. Config via env:
//
//	SCHEME_INDEX     path to schemes-index.json (default /schemes-index.json)
//	BPP_CALLER_URL   adapter caller base (default http://beckn-router:9000/bpp/caller)
//	LISTEN           listen address (default :3002)
//	MAX_RESULTS      max schemes per on_search (default 10)
package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type scheme struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Short         string   `json:"short"`
	Tags          []string `json:"tags"`
	Category      []string `json:"category"`
	Subcategory   []string `json:"subcategory"`
	State         string   `json:"state"`
	Level         string   `json:"level"`
	Beneficiaries []string `json:"beneficiaries"`
	hay           string   // lowercased searchable blob
}

var (
	schemes    []scheme
	callerURL  = envOr("BPP_CALLER_URL", "http://beckn-router:9000/bpp/caller")
	maxResults = atoiOr("MAX_RESULTS", 10)
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func atoiOr(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return d
}

func loadIndex(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read index %s: %v", path, err)
	}
	if err := json.Unmarshal(b, &schemes); err != nil {
		log.Fatalf("parse index: %v", err)
	}
	for i := range schemes {
		s := &schemes[i]
		parts := append([]string{s.Name, s.Short, s.State, s.Level}, s.Tags...)
		parts = append(parts, s.Category...)
		parts = append(parts, s.Subcategory...)
		parts = append(parts, s.Beneficiaries...)
		s.hay = strings.ToLower(strings.Join(parts, " "))
	}
	log.Printf("loaded %d schemes", len(schemes))
}

// search ranks schemes by how many query terms appear, with a name-match boost.
func search(query string) []scheme {
	terms := strings.Fields(strings.ToLower(query))
	type scored struct {
		s     scheme
		score int
	}
	var hits []scored
	for _, s := range schemes {
		score := 0
		nameLower := strings.ToLower(s.Name)
		for _, t := range terms {
			if strings.Contains(s.hay, t) {
				score++
				if strings.Contains(nameLower, t) {
					score += 2
				}
			}
		}
		if len(terms) == 0 || score > 0 {
			hits = append(hits, scored{s, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]scheme, 0, maxResults)
	for i, h := range hits {
		if i >= maxResults {
			break
		}
		out = append(out, h.s)
	}
	return out
}

// collectStrings walks arbitrary JSON collecting string values — used to pull a
// free-text query out of whatever shape the search intent arrives in.
func collectStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		if len(t) > 1 {
			*out = append(*out, t)
		}
	case []any:
		for _, e := range t {
			collectStrings(e, out)
		}
	case map[string]any:
		for _, e := range t {
			collectStrings(e, out)
		}
	}
}

func queryFromIntent(msg map[string]any) string {
	intent, ok := msg["intent"]
	if !ok {
		return ""
	}
	var terms []string
	collectStrings(intent, &terms)
	return strings.Join(terms, " ")
}

func tag(name, value string) map[string]any {
	return map[string]any{"descriptor": map[string]any{"name": name}, "value": value}
}

func onSearchCatalog(results []scheme) map[string]any {
	items := make([]map[string]any, 0, len(results))
	for _, s := range results {
		tags := []map[string]any{}
		if len(s.Category) > 0 {
			tags = append(tags, tag("category", strings.Join(s.Category, ", ")))
		}
		if s.State != "" {
			tags = append(tags, tag("state", s.State))
		}
		if s.Level != "" {
			tags = append(tags, tag("level", s.Level))
		}
		if len(s.Tags) > 0 {
			tags = append(tags, tag("tags", strings.Join(s.Tags, ", ")))
		}
		items = append(items, map[string]any{
			"id": s.ID,
			"descriptor": map[string]any{
				"name":       s.Name,
				"code":       s.Short,
				"short_desc": strings.Join(append(s.Category, s.State), " · "),
			},
			"tags": tags,
		})
	}
	return map[string]any{
		"descriptor": map[string]any{"name": "myScheme registry"},
		"providers": []map[string]any{{
			"id":         "schemes.india.gov.in",
			"descriptor": map[string]any{"name": "Government of India — myScheme"},
			"items":      items,
		}},
	}
}

func webhook(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ctx, _ := req["context"].(map[string]any)
	msg, _ := req["message"].(map[string]any)
	action, _ := ctx["action"].(string)

	// Acknowledge synchronously; the adapter also signs its own ACK to the BAP.
	writeJSON(w, 200, map[string]any{"message": map[string]any{"ack": map[string]any{"status": "ACK"}}})

	if !strings.Contains(strings.ToLower(action), "search") {
		return // this demo answers discovery only
	}
	q := queryFromIntent(msg)
	results := search(q)
	log.Printf("search %q -> %d schemes", q, len(results))

	// Build the on_search callback: echo the context, flip the action.
	onCtx := map[string]any{}
	for k, v := range ctx {
		onCtx[k] = v
	}
	onCtx["action"] = "on_search"
	onCtx["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	body, _ := json.Marshal(map[string]any{
		"context": onCtx,
		"message": map[string]any{"catalog": onSearchCatalog(results)},
	})
	resp, err := http.Post(callerURL+"/on_search", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("on_search post: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("on_search -> %s (%d)", callerURL, resp.StatusCode)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	loadIndex(envOr("SCHEME_INDEX", "/schemes-index.json"))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/webhook", webhook)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"message": "OK!", "schemes": len(schemes)}) })
	// direct search for quick manual testing (not part of the Beckn path)
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, search(r.URL.Query().Get("q")))
	})
	listen := envOr("LISTEN", ":3002")
	log.Printf("scheme-bpp listening on %s, caller=%s", listen, callerURL)
	log.Fatal(http.ListenAndServe(listen, mux))
}
