// scheme-bpp is a minimal Beckn BPP application that makes India's myScheme
// dataset searchable over a Beckn network. It plugs in where the starter kit's
// sandbox-bpp sits: the ONIX adapter forwards an inbound action to
// /api/webhook/<action>, and this app answers a `discover`/`search` by posting
// an `on_discover` catalog of matching schemes to the adapter's caller endpoint
// (which signs + routes it). Search is fuzzy (typo-tolerant).
//
// Pure stdlib. Config via env:
//
//	SCHEME_INDEX     path to schemes-index.json (default /schemes-index.json)
//	BPP_CALLER_URL   adapter caller base (default http://beckn-router:9000/bpp/caller)
//	LISTEN           listen address (default :3002)
//	MAX_RESULTS      max schemes per callback (default 50)
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

	hay       string   // lowercased searchable blob
	nameLower string   // lowercased name
	tokens    []string // distinct words across all fields (for fuzzy matching)
}

var (
	schemes    []scheme
	callerURL  = envOr("BPP_CALLER_URL", "http://beckn-router:9000/bpp/caller")
	maxResults = atoiOr("MAX_RESULTS", 50)
	stopwords  = map[string]bool{"for": true, "the": true, "and": true, "of": true, "to": true,
		"a": true, "an": true, "in": true, "on": true, "with": true, "jsonpath": true,
		"type": true, "expression": true, "scheme": true, "schemes": true}
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

func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range fields {
		if len(w) >= 3 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
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
		blob := strings.Join(parts, " ")
		s.hay = strings.ToLower(blob)
		s.nameLower = strings.ToLower(s.Name)
		s.tokens = tokenize(blob)
	}
	log.Printf("loaded %d schemes", len(schemes))
}

// lev is a bounded Levenshtein distance: returns a value > max as soon as the
// edit distance is known to exceed max (cheap early exit for fuzzy matching).
func lev(a, b string, max int) int {
	la, lb := len(a), len(b)
	if la-lb > max || lb-la > max {
		return max + 1
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < best {
				best = cur[j]
			}
		}
		if best > max {
			return max + 1
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// search ranks schemes: substring hits score high (name boosted), and terms not
// found verbatim fall back to a bounded-edit-distance (fuzzy) token match.
// Returns the page of results plus the total number of matches.
func search(query string) ([]scheme, int) {
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(query)) {
		if len(t) >= 2 && !stopwords[t] {
			terms = append(terms, t)
		}
	}
	type scored struct {
		s     scheme
		score int
	}
	var hits []scored
	for _, s := range schemes {
		score := 0
		for _, t := range terms {
			if strings.Contains(s.hay, t) {
				score += 2
				if strings.Contains(s.nameLower, t) {
					score += 3
				}
				continue
			}
			k := 1
			if len(t) > 6 {
				k = 2
			}
			for _, tok := range s.tokens {
				if lev(t, tok, k) <= k {
					score++
					break
				}
			}
		}
		if score > 0 || len(terms) == 0 {
			hits = append(hits, scored{s, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].s.Name < hits[j].s.Name
	})
	total := len(hits)
	out := make([]scheme, 0, maxResults)
	for i, h := range hits {
		if i >= maxResults {
			break
		}
		out = append(out, h.s)
	}
	return out, total
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

// schemeResource maps one scheme to a Beckn Resource (id + descriptor).
func schemeResource(s scheme) map[string]any {
	meta := []string{}
	if len(s.Category) > 0 {
		meta = append(meta, strings.Join(s.Category, ", "))
	}
	if s.State != "" {
		meta = append(meta, s.State)
	}
	if s.Level != "" {
		meta = append(meta, s.Level)
	}
	return map[string]any{
		"id": s.ID,
		"descriptor": map[string]any{
			"name":      s.Name,
			"code":      s.Short,
			"shortDesc": strings.Join(meta, " · "),
			"longDesc":  strings.Join(s.Tags, ", "),
		},
	}
}

// onDiscoverCatalogs builds the message.catalogs array for an on_discover.
func onDiscoverCatalogs(results []scheme, total int) []map[string]any {
	resources := make([]map[string]any, 0, len(results))
	for _, s := range results {
		resources = append(resources, schemeResource(s))
	}
	return []map[string]any{{
		"id":       "myscheme-registry",
		"isActive": true,
		"descriptor": map[string]any{
			"name":      "myScheme registry",
			"shortDesc": strconv.Itoa(total) + " matching schemes",
		},
		"provider": map[string]any{
			"id":         "schemes.india.gov.in",
			"descriptor": map[string]any{"name": "Government of India — myScheme"},
		},
		"resources": resources,
	}}
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

	low := strings.ToLower(action)
	if !strings.Contains(low, "search") && !strings.Contains(low, "discover") {
		return // this demo answers discovery only
	}
	q := queryFromIntent(msg)
	results, total := search(q)
	log.Printf("%s %q -> %d/%d schemes", action, q, len(results), total)

	onAction := "on_" + action
	onCtx := map[string]any{}
	for k, v := range ctx {
		onCtx[k] = v
	}
	onCtx["action"] = onAction
	onCtx["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	body, _ := json.Marshal(map[string]any{
		"context": onCtx,
		"message": map[string]any{"catalogs": onDiscoverCatalogs(results, total)},
	})
	resp, err := http.Post(callerURL+"/"+onAction, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("%s post: %v", onAction, err)
		return
	}
	resp.Body.Close()
	log.Printf("%s -> %s (%d)", onAction, callerURL, resp.StatusCode)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	loadIndex(envOr("SCHEME_INDEX", "/schemes-index.json"))
	mux := http.NewServeMux()
	// the adapter posts to /api/webhook/<action>; the action also arrives in the body
	mux.HandleFunc("POST /api/webhook", webhook)
	mux.HandleFunc("POST /api/webhook/{action}", webhook)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"message": "OK!", "schemes": len(schemes)})
	})
	// direct search for quick manual testing (not part of the Beckn path)
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		results, total := search(r.URL.Query().Get("q"))
		writeJSON(w, 200, map[string]any{"total": total, "results": results})
	})
	listen := envOr("LISTEN", ":3002")
	log.Printf("scheme-bpp listening on %s, caller=%s", listen, callerURL)
	log.Fatal(http.ListenAndServe(listen, mux))
}
