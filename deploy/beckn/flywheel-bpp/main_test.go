package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The whole point of the service: the callback must arrive at OUR caller, not
// at the upstream's. Pointing the adapter straight at the upstream fails
// exactly here, so this is the test that justifies the service existing.
func TestDiscoverAnswersThroughOurCallerWithTheUpstreamsCatalog(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("domain"); got != "mandi" {
			t.Errorf("upstream domain = %q, want mandi", got)
		}
		if got := r.URL.Query().Get("q"); got != "onion pune" {
			t.Errorf("upstream q = %q, want %q", got, "onion pune")
		}
		io.WriteString(w, `{"total":7,"response":{"message":{"catalogs":[{"resources":[{"id":"onion"}]}]}}}`)
	}))
	defer up.Close()

	got := make(chan []byte, 1)
	caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/on_discover" {
			t.Errorf("caller path = %q, want /on_discover", r.URL.Path)
		}
		got <- b
	}))
	defer caller.Close()

	p := &proxy{caller: caller.URL, upstream: up.URL, client: &http.Client{Timeout: 5 * time.Second}}
	rec := httptest.NewRecorder()
	p.webhook(rec, httptest.NewRequest("POST", "/api/webhook/discover", strings.NewReader(
		`{"context":{"action":"discover","domain":"beckn-demo:mandi","transactionId":"t1"},
		  "message":{"intent":{"filters":{"attributes":{"commodity":"onion","market":"pune"}}}}}`)))

	// The ack is synchronous and must not wait on the upstream.
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ACK"`) {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}

	var body []byte
	select {
	case body = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("no callback reached our caller")
	}

	var env struct {
		Context map[string]any `json:"context"`
		Message struct {
			Catalogs []map[string]any `json:"catalogs"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("callback json: %v", err)
	}
	if env.Context["action"] != "on_discover" {
		t.Errorf("action = %v, want on_discover", env.Context["action"])
	}
	// The rest of the context is carried through untouched: the BAP correlates
	// the callback to its request by transactionId, and rewriting it would
	// orphan the response.
	if env.Context["transactionId"] != "t1" {
		t.Errorf("transactionId = %v, want t1", env.Context["transactionId"])
	}
	if len(env.Message.Catalogs) != 1 {
		t.Fatalf("catalogs = %d, want the upstream's 1", len(env.Message.Catalogs))
	}
}

// An upstream that fails must not produce a callback. A half-built on_discover
// would be rejected downstream as a schema failure and blame the wrong service.
func TestUpstreamFailureSendsNoCallback(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer up.Close()

	called := make(chan struct{}, 1)
	caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called <- struct{}{} }))
	defer caller.Close()

	p := &proxy{caller: caller.URL, upstream: up.URL, client: &http.Client{Timeout: 5 * time.Second}}
	p.answer(map[string]any{"action": "discover", "domain": "beckn-demo:schemes"}, map[string]any{}, "discover")

	select {
	case <-called:
		t.Fatal("posted a callback for a failed upstream search")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDomainOfMapsTheNetworkDomainToTheUpstreamParameter(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"beckn-demo:schemes", "schemes"},
		{"beckn-demo:weather", "weather"},
		{"beckn-demo:mandi", "mandi"},
		{"beckn-demo:news", "news"},
		{"schemes", "schemes"},
		{"beckn-demo:retail", "schemes"}, // unknown falls back rather than 400s
		{"", "schemes"},
	} {
		if got := domainOf(map[string]any{"domain": tc.in}); got != tc.want {
			t.Errorf("domainOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Go randomises map iteration, so an unsorted flatten makes the same intent
// produce a different query on every call and the upstream's fuzzy match score
// differently each time.
func TestQueryFromIntentIsStableAcrossCalls(t *testing.T) {
	msg := map[string]any{"intent": map[string]any{
		"zulu": "last", "alpha": "first", "mid": []any{"b", "a"},
	}}
	first := queryFromIntent(msg)
	if first != "first b a last" {
		t.Fatalf("query = %q, want %q", first, "first b a last")
	}
	for i := 0; i < 50; i++ {
		if got := queryFromIntent(msg); got != first {
			t.Fatalf("call %d = %q, want stable %q", i, got, first)
		}
	}
}

// Anything that is not a discover is acked and dropped: the upstream has no
// select/init/confirm to forward to, and inventing one would claim an order
// path this demo does not have.
func TestNonDiscoverActionsAreAckedAndIgnored(t *testing.T) {
	called := make(chan struct{}, 1)
	caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called <- struct{}{} }))
	defer caller.Close()

	p := &proxy{caller: caller.URL, upstream: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	rec := httptest.NewRecorder()
	p.webhook(rec, httptest.NewRequest("POST", "/api/webhook/confirm",
		strings.NewReader(`{"context":{"action":"confirm"},"message":{}}`)))

	if !strings.Contains(rec.Body.String(), `"ACK"`) {
		t.Fatalf("confirm was not acked: %s", rec.Body.String())
	}
	select {
	case <-called:
		t.Fatal("confirm produced a callback")
	case <-time.After(200 * time.Millisecond):
	}
}

// Beckn 2.0.0 requires catalogs[].provider and the BPP caller rejects a
// callback without one. The upstream supplies it for schemes and omits it for
// the provider domains, so filling the gap is what makes those three usable.
func TestCatalogsWithoutAProviderGetOneBeforeTheyAreSigned(t *testing.T) {
	cats := []any{map[string]any{"id": "weather", "resources": []any{}}}
	got := withProvider(cats, "weather")[0].(map[string]any)

	p, ok := got["provider"].(map[string]any)
	if !ok {
		t.Fatalf("no provider added: %v", got)
	}
	if p["id"] != "weather.theflywheel.in" {
		t.Errorf("provider id = %v, want the subscriber it is registered under", p["id"])
	}
	if got["isActive"] != true {
		t.Errorf("isActive = %v, want true", got["isActive"])
	}
}

// Where the upstream names a provider it is the truthful one and more specific
// than anything we could invent, so it must survive untouched.
func TestAnUpstreamProviderIsNeverOverwritten(t *testing.T) {
	theirs := map[string]any{"id": "schemes.india.gov.in"}
	cats := []any{map[string]any{"provider": theirs, "resources": []any{}}}
	got := withProvider(cats, "schemes")[0].(map[string]any)
	if fmt.Sprintf("%v", got["provider"]) != fmt.Sprintf("%v", theirs) {
		t.Errorf("provider = %v, want the upstream's %v", got["provider"], theirs)
	}
}

// A conformant Beckn 2.0.0 discover must carry filters.type — the adapter NACKs
// it otherwise — so flattening every string searches for the expression
// language as well as the query. This is the shape every real request has.
func TestQueryComesFromTheExpressionNotTheWholeFilter(t *testing.T) {
	msg := map[string]any{"intent": map[string]any{
		"filters": map[string]any{"type": "jsonpath", "expression": "Pune"},
	}}
	if got := queryFromIntent(msg); got != "Pune" {
		t.Errorf("query = %q, want %q — the expression language is not a search term", got, "Pune")
	}
}

// An intent carrying its terms somewhere other than an expression is still
// legitimate; dropping it would turn a working search into an empty one.
func TestAnIntentWithoutAnExpressionStillFlattens(t *testing.T) {
	msg := map[string]any{"intent": map[string]any{
		"filters": map[string]any{"attributes": map[string]any{"commodity": "onion", "market": "pune"}},
	}}
	if got := queryFromIntent(msg); got != "onion pune" {
		t.Errorf("query = %q, want %q", got, "onion pune")
	}
}
