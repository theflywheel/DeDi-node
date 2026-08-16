// flywheel-bpp is the application behind our ONIX BPP adapter.
//
// It holds no data. The schemes corpus, the weather/mandi/news providers and
// the AI reranker all live in the flywheel demo at schemes.proto.theflywheel.in,
// which this service calls as an upstream provider — so registering that demo on
// our network costs one record and this shim, not a copy of its dataset.
//
// The shim exists for exactly one reason. The demo's own Beckn entry point
// (POST /api/webhook/{action}) acks synchronously and then posts the
// on_discover to *its* BPP_CALLER_URL, an env var baked to the beckn-router on
// its host. Pointed straight at it, our adapter would get an ack and never a
// callback. So we take the webhook here and post the callback to our caller.
//
// What it does not do is rebuild a catalog: the demo's GET /beckn/search returns
// the finished on_discover envelope for every domain it serves, so the response
// body our BAP sees is the demo's own, lifted whole.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", envOr("LISTEN", ":3002"), "listen address")
	caller := flag.String("caller", envOr("BPP_CALLER_URL", ""), "our adapter's caller base, where callbacks are posted")
	upstream := flag.String("upstream", envOr("UPSTREAM_URL", "https://schemes.proto.theflywheel.in"), "the provider this BPP fronts")
	flag.Parse()

	p := &proxy{caller: strings.TrimRight(*caller, "/"), upstream: strings.TrimRight(*upstream, "/"),
		// Generous: the AI path on the upstream takes tens of seconds, and a
		// timeout here is indistinguishable to the BAP from a provider with
		// nothing to say.
		client: &http.Client{Timeout: 90 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/webhook", p.webhook)
	mux.HandleFunc("POST /api/webhook/{action}", p.webhook)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{$}", p.index)

	log.Printf("flywheel-bpp on %s: upstream %s, caller %s", *listen, p.upstream, p.caller)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

type proxy struct {
	caller   string
	upstream string
	client   *http.Client
}

// webhook answers the adapter, then does the work.
//
// The ack goes out before the upstream is called on purpose: Beckn's transport
// is two one-way messages, and the adapter is entitled to an ack inside its
// response header timeout (5s in the BPP config) while a search — especially the
// AI one — is not obliged to finish in that window.
func (p *proxy) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var env struct {
		Context map[string]any `json:"context"`
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"message":{"ack":{"status":"ACK"}}}`))

	action, _ := env.Context["action"].(string)
	if !strings.Contains(strings.ToLower(action), "discover") && !strings.Contains(strings.ToLower(action), "search") {
		log.Printf("ignoring action %q", action)
		return
	}
	go p.answer(env.Context, env.Message, action)
}

func (p *proxy) answer(ctx, msg map[string]any, action string) {
	dom := domainOf(ctx)
	q := queryFromIntent(msg)
	catalogs, total, err := p.search(dom, q)
	if err != nil {
		// Deliberately no callback on failure. A malformed on_discover would be
		// rejected by the BAP's adapter as an unsigned-schema failure and tell
		// the operator nothing; silence plus this line is the honest signal, and
		// the BAP's own ttl is what ends the wait.
		log.Printf("upstream %s %q: %v", dom, q, err)
		return
	}

	onAction := "on_" + action
	onCtx := map[string]any{}
	for k, v := range ctx {
		onCtx[k] = v
	}
	onCtx["action"] = onAction
	onCtx["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	out, _ := json.Marshal(map[string]any{
		"context": onCtx,
		"message": map[string]any{"catalogs": catalogs},
	})

	resp, err := p.client.Post(p.caller+"/"+onAction, "application/json", bytes.NewReader(out))
	if err != nil {
		log.Printf("%s post: %v", onAction, err)
		return
	}
	resp.Body.Close()
	log.Printf("%s %q -> %g result(s), caller %d", dom, q, total, resp.StatusCode)
}

// search asks the upstream for the on_discover it would have produced and
// returns its catalogs verbatim.
func (p *proxy) search(domain, q string) ([]any, float64, error) {
	u := fmt.Sprintf("%s/beckn/search?domain=%s&q=%s", p.upstream, url.QueryEscape(domain), url.QueryEscape(q))
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	var d struct {
		Total    float64 `json:"total"`
		Error    string  `json:"error"`
		Response struct {
			Message struct {
				Catalogs []any `json:"catalogs"`
			} `json:"message"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, 0, err
	}
	if d.Error != "" {
		return nil, 0, fmt.Errorf("upstream: %s", d.Error)
	}
	return withProvider(d.Response.Message.Catalogs, domain), d.Total, nil
}

// providers names the upstream source behind each domain, by the subscriber id
// it is registered under in our own registry.
var providers = map[string][2]string{
	"schemes": {"schemes.india.gov.in", "Government of India · myScheme"},
	"weather": {"weather.theflywheel.in", "Weather Union + Open-Meteo"},
	"mandi":   {"mandi.theflywheel.in", "CommodityOnline + MandiGuru"},
	"news":    {"news.theflywheel.in", "Event Registry · agriculture news"},
}

// withProvider fills in the catalog's provider when the upstream left it out.
//
// Beckn 2.0.0 requires it and the BPP caller rejects the callback without one
// ("property \"provider\" is missing"). The upstream sets it for schemes and
// omits it for weather, mandi and news, which never shows up on its own stack
// because only its schemes path goes through an adapter — the UI reads
// /beckn/search directly and validates nothing.
//
// Naming the provider is ours to do rather than a workaround: on this network
// we are the BPP publishing this catalog, and an unattributed one would tell a
// consumer nothing about where the data came from.
func withProvider(catalogs []any, domain string) []any {
	p, known := providers[domain]
	if !known {
		return catalogs
	}
	for _, c := range catalogs {
		cat, ok := c.(map[string]any)
		if !ok {
			continue
		}
		// Never overwrite: where the upstream names a provider, that is the
		// truthful one and it is more specific than anything we could supply.
		if _, has := cat["provider"]; !has {
			cat["provider"] = map[string]any{
				"id":         p[0],
				"descriptor": map[string]any{"name": p[1]},
			}
		}
		if _, has := cat["isActive"]; !has {
			cat["isActive"] = true
		}
	}
	return catalogs
}

// domainOf maps the Beckn context domain onto the upstream's domain parameter.
//
// The network speaks "beckn-demo:schemes"; the upstream's own read endpoint
// takes the bare tail. An unknown domain falls back to schemes rather than
// erroring, because the upstream answers that for any query and a demo that
// returns something is more useful than one that returns a 400.
func domainOf(ctx map[string]any) string {
	d, _ := ctx["domain"].(string)
	if i := strings.LastIndex(d, ":"); i >= 0 {
		d = d[i+1:]
	}
	switch d {
	case "schemes", "weather", "mandi", "news":
		return d
	default:
		return "schemes"
	}
}

// queryFromIntent recovers what the user actually asked for.
//
// filters.expression is preferred over flattening because Beckn 2.0.0 requires
// filters.type alongside it — a schema-valid discover is NACKed without it
// ("property \"type\" is missing"). Flattening every string therefore drops the
// expression language into the query on every conformant request: "Pune" is
// searched for as "Pune jsonpath". Fuzzy scheme matching shrugs that off;
// geocoding a city and looking up a commodity do not.
//
// The upstream BPP flattens unconditionally and never notices, because its own
// UI reads /beckn/search directly and never builds a protocol envelope. On the
// signed path there is no such escape, so this diverges deliberately.
//
// Anything else still flattens: an intent that carries its terms in descriptors
// or attributes rather than an expression is legitimate, and dropping it would
// turn a working search into an empty one.
func queryFromIntent(msg map[string]any) string {
	intent, ok := msg["intent"].(map[string]any)
	if !ok {
		if _, present := msg["intent"]; !present {
			return ""
		}
		var terms []string
		collectStrings(msg["intent"], &terms)
		return strings.Join(terms, " ")
	}
	if filters, ok := intent["filters"].(map[string]any); ok {
		if expr, ok := filters["expression"].(string); ok && strings.TrimSpace(expr) != "" {
			return strings.TrimSpace(expr)
		}
	}
	var terms []string
	collectStrings(intent, &terms)
	return strings.Join(terms, " ")
}

func collectStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			*out = append(*out, s)
		}
	case []any:
		for _, x := range t {
			collectStrings(x, out)
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			collectStrings(t[k], out)
		}
	}
}

// sortedKeys keeps the flattened query stable. Go randomises map iteration, so
// without this the same intent produces a different query string on every call
// and the upstream's fuzzy match scores differently each time.
func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func (p *proxy) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>flywheel-bpp</title>
<body style="font-family:monospace;max-width:60em;margin:2em auto;line-height:1.5;padding:0 1em">
<h1>flywheel-bpp</h1>
<p>The application behind this network's ONIX BPP adapter. It answers Beckn
<code>discover</code> by asking <a href="%s">%s</a> — registered on this network as a
provider — and posts the resulting <code>on_discover</code> back through our own caller.</p>
<p>Identities and signing keys resolve against our DeDi registry, not the
upstream's. <code>POST /api/webhook/{action}</code> is the Beckn entry point;
<code>GET /health</code> is the liveness check.</p>`, p.upstream, p.upstream)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
