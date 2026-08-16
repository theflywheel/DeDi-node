package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func seedQuery(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "flywheel", PayloadRaw: []byte(`{"description":"net"}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "registry", Namespace: "flywheel", Registry: "participants", PayloadRaw: []byte(`{"schema":{"type":"object"}}`), CreatedBy: "seed"})
	must(store.AppendInput{EntryType: "registry", Namespace: "flywheel", Registry: "witnesses", PayloadRaw: []byte(`{}`), CreatedBy: "seed"})
	for i := 0; i < 3; i++ {
		must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: fmt.Sprintf("bap%d.example.com", i), PayloadRaw: []byte(`{"role":"BAP"}`), CreatedBy: "seed"})
	}
	must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bap0.example.com", PayloadRaw: []byte(`{"role":"BAP","v":2}`), CreatedBy: "seed"})
}

func TestQueryNamespaceListsRegistries(t *testing.T) {
	srv, s, _ := testServer(t)
	seedQuery(t, s)
	m := getJSON(t, srv.URL+"/dedi/query/flywheel", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["namespace_id"] != "flywheel" || data["total_registries"].(float64) != 2 {
		t.Fatalf("namespace query header: %v", data)
	}
	regs := data["registries"].([]any)
	if len(regs) != 2 {
		t.Fatalf("registries: %v", regs)
	}
	// record_count on participants registry
	for _, r := range regs {
		rm := r.(map[string]any)
		if rm["registry_name"] == "participants" && rm["record_count"].(float64) != 3 {
			t.Fatalf("participants record_count: %v", rm["record_count"])
		}
	}
}

func TestQueryRegistryListsLatestRecordsOnce(t *testing.T) {
	srv, s, _ := testServer(t)
	seedQuery(t, s)
	m := getJSON(t, srv.URL+"/dedi/query/flywheel/participants", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["total_records"].(float64) != 3 {
		t.Fatalf("total_records: %v", data["total_records"])
	}
	recs := data["records"].([]any)
	if len(recs) != 3 {
		t.Fatalf("records: %d", len(recs))
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.(map[string]any)["record_name"].(string)] = true
	}
	if !seen["bap0.example.com"] || !seen["bap1.example.com"] || !seen["bap2.example.com"] {
		t.Fatalf("record names: %v", seen)
	}
}

func TestQueryFiltersAndPagination(t *testing.T) {
	srv, s, _ := testServer(t)
	seedQuery(t, s)
	// name filter
	m := getJSON(t, srv.URL+"/dedi/query/flywheel/participants?name=bap1", http.StatusOK)
	if m["data"].(map[string]any)["total_records"].(float64) != 1 {
		t.Fatalf("name filter: %v", m["data"])
	}
	// pagination: page_size=2 → page 1 has 2, page 2 has 1, totals stable
	m = getJSON(t, srv.URL+"/dedi/query/flywheel/participants?page_size=2&page=2&sort=name", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["total_records"].(float64) != 3 || data["total_pages"].(float64) != 2 || len(data["records"].([]any)) != 1 {
		t.Fatalf("pagination: %v", data)
	}
	// invalid sort
	mm := getJSON(t, srv.URL+"/dedi/query/flywheel/participants?sort=bogus", http.StatusBadRequest)
	if mm["code"] != "INVALID_REQUEST" {
		t.Fatalf("invalid sort code: %v", mm["code"])
	}
	// unknown namespace
	getJSON(t, srv.URL+"/dedi/query/nope", http.StatusNotFound)
}

func TestQueryStatusStateFilterValidation(t *testing.T) {
	srv, s, _ := testServer(t)
	seedQuery(t, s)

	cases := []struct {
		name       string
		path       string
		wantStatus int
	}{
		// namespace query: `status` param, spec enum [active, inactive]
		{"namespace status spec value", "/dedi/query/flywheel?status=active", http.StatusOK},
		{"namespace status stored value", "/dedi/query/flywheel?status=revoked", http.StatusOK},
		{"namespace status garbage", "/dedi/query/flywheel?status=bogus", http.StatusBadRequest},
		// registry query: `state` param, spec enum [live]
		{"registry state spec value", "/dedi/query/flywheel/participants?state=live", http.StatusOK},
		{"registry state stored value", "/dedi/query/flywheel/participants?state=draft", http.StatusOK},
		{"registry state garbage", "/dedi/query/flywheel/participants?state=bogus", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := getJSON(t, srv.URL+tc.path, tc.wantStatus)
			if tc.wantStatus == http.StatusBadRequest && m["code"] != "INVALID_REQUEST" {
				t.Fatalf("invalid filter code: %v", m["code"])
			}
		})
	}
}

func TestQueryPageSizeCapEchoedCorrectly(t *testing.T) {
	srv, s, _ := testServer(t)
	seedQuery(t, s)
	m := getJSON(t, srv.URL+"/dedi/query/flywheel/participants?page_size=1000", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["page_size"].(float64) != float64(100) {
		t.Fatalf("page_size: %v", data["page_size"])
	}
	if data["total_pages"].(float64) != float64(1) {
		t.Fatalf("total_pages: %v", data["total_pages"])
	}
	if data["total_records"].(float64) != float64(3) {
		t.Fatalf("total_records: %v", data["total_records"])
	}
}
