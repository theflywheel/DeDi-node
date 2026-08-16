package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func seedDiscoverable(t *testing.T, s *Server, ns, reg, name, payload, state string) {
	t.Helper()
	if _, err := s.Store.Append(t.Context(), store.AppendInput{
		EntryType: "record", Namespace: ns, Registry: reg, RecordName: name,
		PayloadRaw: []byte(payload), State: state, CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

type discoveryResponse struct {
	Data struct {
		Domain       string          `json:"domain"`
		Total        int             `json:"total"`
		Participants []discoveredDTO `json:"participants"`
	} `json:"data"`
}

func discover(t *testing.T, srv *httptest.Server, path string) (*http.Response, discoveryResponse) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out discoveryResponse
	json.Unmarshal(body, &out)
	return resp, out
}

func discoveryServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	srv, api, _ := parentServer(t, "beckn")
	if _, err := api.Store.Append(t.Context(), store.AppendInput{
		EntryType: "registry", Namespace: "beckn", Registry: "subscribers",
		PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		// The namespace is created by parentServer's fixture only if something
		// published into it, so create both here.
		if _, nsErr := api.Store.Append(t.Context(), store.AppendInput{
			EntryType: "namespace", Namespace: "beckn", PayloadRaw: []byte(`{}`), CreatedBy: "test",
		}); nsErr != nil {
			t.Fatal(nsErr)
		}
		if _, err := api.Store.Append(t.Context(), store.AppendInput{
			EntryType: "registry", Namespace: "beckn", Registry: "subscribers",
			PayloadRaw: []byte(`{}`), CreatedBy: "test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, api
}

// The read that makes routing possible: who serves this domain, and at what
// URL. Until now the `url` in every participant record was never read by
// anything.
func TestDiscoveryReturnsWhoServesADomain(t *testing.T) {
	srv, api := discoveryServer(t)
	seedDiscoverable(t, api, "beckn", "subscribers", "bap-1",
		`{"subscriber_id":"bap.example","domain":"retail","status":"SUBSCRIBED",
		  "url":"https://bap.example/beckn","type":"BAP"}`, "live")
	seedDiscoverable(t, api, "beckn", "subscribers", "other",
		`{"subscriber_id":"m.example","domain":"mobility","status":"SUBSCRIBED","url":"https://m.example"}`, "live")

	resp, out := discover(t, srv, "/dedi/query/beckn/subscribers?domain=retail")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
	if out.Data.Total != 1 || len(out.Data.Participants) != 1 {
		t.Fatalf("want one participant, got %+v", out.Data.Participants)
	}
	p := out.Data.Participants[0]
	if p.SubscriberID != "bap.example" || p.URL != "https://bap.example/beckn" || p.Type != "BAP" {
		t.Fatalf("unexpected participant: %+v", p)
	}
	// The list is a starting point, not an answer to be trusted — this node
	// assembled it, and only the record and its proof say what was published.
	if p.LookupURL == "" {
		t.Error("no lookup URL: a caller has no pointed way to verify what it was handed")
	}
}

// At the HTTP layer, not only in the store: this is the surface that decides
// where traffic is *sent*, and a revoked participant still being offered is a
// worse failure than one still being verifiable.
func TestDiscoveryStopsReturningARevokedParticipant(t *testing.T) {
	srv, api := discoveryServer(t)
	payload := `{"subscriber_id":"bap.example","domain":"retail","status":"SUBSCRIBED","url":"https://bap.example"}`
	seedDiscoverable(t, api, "beckn", "subscribers", "bap-1", payload, "live")

	if _, out := discover(t, srv, "/dedi/query/beckn/subscribers?domain=retail"); out.Data.Total != 1 {
		t.Fatalf("setup: %+v", out.Data)
	}

	seedDiscoverable(t, api, "beckn", "subscribers", "bap-1", payload, "revoked")

	_, out := discover(t, srv, "/dedi/query/beckn/subscribers?domain=retail")
	if out.Data.Total != 0 {
		t.Fatalf("a revoked participant is still being offered as a destination: %+v", out.Data.Participants)
	}
}

// The answer goes stale exactly when a revocation lands. A long max-age here
// would reintroduce the staleness window push exists to close, on the surface
// where it matters most.
func TestDiscoveryIsNotCachedForLong(t *testing.T) {
	srv, api := discoveryServer(t)
	seedDiscoverable(t, api, "beckn", "subscribers", "bap-1",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED","url":"https://a"}`, "live")

	resp, _ := discover(t, srv, "/dedi/query/beckn/subscribers?domain=retail")
	cc := resp.Header.Get("Cache-Control")
	if cc == "" || strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control is %q; a discovery answer must not be pinned", cc)
	}
	if !strings.Contains(cc, "max-age="+itoa(api.TTL)) {
		t.Fatalf("Cache-Control is %q, want the node's short TTL (%d)", cc, api.TTL)
	}
}

// /dedi/query is spec-defined and deliberately never reached into the payload.
// The extension is opt-in by the parameter, so a spec-conformant client cannot
// be surprised by a different response shape.
func TestQueryWithoutTheDomainParameterIsUnchanged(t *testing.T) {
	srv, api := discoveryServer(t)
	seedDiscoverable(t, api, "beckn", "subscribers", "bap-1",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED","url":"https://a"}`, "live")

	resp, err := http.Get(srv.URL + "/dedi/query/beckn/subscribers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Data["records"]; !ok {
		t.Fatalf("the spec query no longer returns `records`: %s", body)
	}
	if _, ok := out.Data["participants"]; ok {
		t.Fatalf("the extension leaked into the plain query: %s", body)
	}
}

// A caller asked about one registry. ServingDomain searches every eligible
// namespace, which is what discovery means, but the answer must not hand back
// another registry's participants under this one's name.
func TestDiscoveryIsScopedToTheRegistryInThePath(t *testing.T) {
	srv, api := discoveryServer(t)
	if _, err := api.Store.Append(t.Context(), store.AppendInput{
		EntryType: "registry", Namespace: "beckn", Registry: "gateways",
		PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	seedDiscoverable(t, api, "beckn", "subscribers", "in-scope",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED","url":"https://a"}`, "live")
	seedDiscoverable(t, api, "beckn", "gateways", "out-of-scope",
		`{"subscriber_id":"b","domain":"retail","status":"SUBSCRIBED","url":"https://b"}`, "live")

	_, out := discover(t, srv, "/dedi/query/beckn/subscribers?domain=retail")
	if out.Data.Total != 1 || out.Data.Participants[0].RecordName != "in-scope" {
		t.Fatalf("another registry's participants leaked in: %+v", out.Data.Participants)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
