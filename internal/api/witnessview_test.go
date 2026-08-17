package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// A real target origin has a slash in it — it is the log origin, `host/log`.
// Using one here rather than a tidy `target.test` is deliberate: the path
// encoding of that slash is the part most likely to break.
const testTargetOrigin = "b.example.com/log"

func seedVerdict(t *testing.T, s *store.Store, origin string, payload string, state string) {
	t.Helper()
	ctx := context.Background()
	must := func(in store.AppendInput) {
		t.Helper()
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
	if _, err := s.Resolve(ctx, "namespace", witnessNamespace, "", "", nil, nil); err != nil {
		must(store.AppendInput{EntryType: "namespace", Namespace: witnessNamespace,
			PayloadRaw: []byte(`{"description":"checkpoints this node has independently witnessed"}`),
			CreatedBy:  "witness"})
	}
	must(store.AppendInput{EntryType: "registry", Namespace: witnessNamespace, Registry: origin,
		PayloadRaw: []byte(`{"description":"witnessed checkpoints of ` + origin +
			`","target":"https://` + origin + `/dedi"}`),
		CreatedBy: "witness"})
	if payload != "" {
		must(store.AppendInput{EntryType: "record", Namespace: witnessNamespace, Registry: origin,
			RecordName: "checkpoint", PayloadRaw: []byte(payload), State: state, CreatedBy: "witness"})
	}
}

func soundVerdict(size int64) string {
	b, _ := json.Marshal(map[string]any{
		"target": "https://b.example.com/dedi", "size": size,
		"root": "Rk9PQkFSRk9PQkFSRk9PQkFSRk9PQkFSRk9PQkFSRk8=", "consistency_ok": true,
	})
	return string(b)
}

// The headline behaviour: a reader who knows nothing about `_witness`, nothing
// about percent-encoding a log origin, and nothing about `?internal=1` can
// still read what this node has verified. That is the whole of issue #27.
func TestWitnessVerdictIsReadableWithoutInsideKnowledge(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1746), "live")

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	data := m["data"].(map[string]any)
	targets := data["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	got := targets[0].(map[string]any)
	if got["origin"] != testTargetOrigin {
		t.Errorf("origin = %v, want %v", got["origin"], testTargetOrigin)
	}
	if got["consistency_ok"] != true {
		t.Errorf("consistency_ok = %v, want true", got["consistency_ok"])
	}
	if got["size"].(float64) != 1746 {
		t.Errorf("size = %v, want 1746", got["size"])
	}
	if got["target_url"] != "https://"+testTargetOrigin+"/dedi" {
		t.Errorf("target_url = %v", got["target_url"])
	}
	if data["consistent"].(float64) != 1 {
		t.Errorf("consistent = %v, want 1", data["consistent"])
	}
}

// The verdict_url the list hands out has to actually resolve. It is generated
// with url.PathEscape over an origin containing a slash, so this is the test
// that fails if that escaping is ever dropped or double-applied.
func TestVerdictURLFromTheListResolves(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1746), "live")

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	target := m["data"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	link, isStr := target["verdict_url"].(string)
	if !isStr || link == "" {
		t.Fatal("no verdict_url on the listed target")
	}
	one := getJSON(t, srv.URL+link, http.StatusOK)
	if one["data"].(map[string]any)["origin"] != testTargetOrigin {
		t.Fatalf("following verdict_url landed on %v", one["data"])
	}
}

// The singular form carries an inclusion proof, which is the reason it exists
// separately from the list: without it the verdict is an assertion over HTTP
// rather than something this node has committed to in its own log.
func TestSingleVerdictCarriesItsInclusionProof(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1746), "live")

	m := getJSON(t, srv.URL+"/dedi/witness/"+url.PathEscape(testTargetOrigin), http.StatusOK)
	proof, have := m["proof"].(map[string]any)
	if !have {
		t.Fatalf("no proof on a single verdict: %v", m)
	}
	if proof["checkpoint"] == "" || proof["checkpoint"] == nil {
		t.Error("proof carries no checkpoint, so nothing anchors it")
	}
	leaf := proof["leaf"].(map[string]any)
	if leaf["namespace"] != witnessNamespace || leaf["record_name"] != "checkpoint" {
		t.Errorf("proof is for the wrong leaf: %v", leaf)
	}
}

// A failed verdict is the entire point of witnessing, so it must be impossible
// to miss: the flag flips, the state says revoked, and the detail explaining
// what was caught is never summarised away.
func TestAlarmingVerdictSurfacesInFull(t *testing.T) {
	srv, s, _ := testServer(t)
	payload, _ := json.Marshal(map[string]any{
		"target": "https://b.example.com/dedi", "size": 9, "root": "Zm9v",
		"consistency_ok": false,
		"detail":         "root changed while tree size stayed at 9: history was rewritten in place",
	})
	seedVerdict(t, s, testTargetOrigin, string(payload), "revoked")

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	data := m["data"].(map[string]any)
	got := data["targets"].([]any)[0].(map[string]any)
	if got["consistency_ok"] != false {
		t.Errorf("consistency_ok = %v, want false", got["consistency_ok"])
	}
	if got["state"] != "revoked" {
		t.Errorf("state = %v, want revoked", got["state"])
	}
	if got["detail"] == nil {
		t.Error("the detail explaining the alarm was dropped")
	}
	if data["consistent"].(float64) != 0 {
		t.Errorf("consistent = %v, want 0 — a failed verdict must not count as sound", data["consistent"])
	}
}

// "Never checked" must not read as "checked and fine". A caller reducing with
// `.consistency_ok // true` over a target that has no verdict yet would
// otherwise conclude the target is sound.
func TestTargetWithNoVerdictIsNotReportedAsSound(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, "", "")

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	data := m["data"].(map[string]any)
	got := data["targets"].([]any)[0].(map[string]any)
	if got["witnessed"] != false {
		t.Errorf("witnessed = %v, want false", got["witnessed"])
	}
	if _, present := got["consistency_ok"]; present {
		t.Error("consistency_ok is present on a target that has never been verified")
	}
	if data["consistent"].(float64) != 0 {
		t.Errorf("consistent = %v, want 0", data["consistent"])
	}
}

// Witnessing nobody is a configuration, not a fault. A 404 here would be read
// as "this node is too old to have the endpoint" — a different fact needing a
// different remedy.
func TestWitnessingNobodyAnswersEmptyRatherThan404(t *testing.T) {
	srv, _, _ := testServer(t)

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["total"].(float64) != 0 {
		t.Errorf("total = %v, want 0", data["total"])
	}
	if len(data["targets"].([]any)) != 0 {
		t.Errorf("targets = %v, want empty", data["targets"])
	}
}

func TestUnknownWitnessTargetIs404(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1), "live")

	m := getJSON(t, srv.URL+"/dedi/witness/"+url.PathEscape("nobody.example/log"), http.StatusNotFound)
	if m["code"] != "NOT_FOUND" {
		t.Errorf("code = %v, want NOT_FOUND", m["code"])
	}
}

// Publishing verdicts properly must not quietly un-hide the bookkeeping
// namespace from the spec read plane. The new surface is an addition, not a
// relaxation, and this is what would catch someone "simplifying" it later.
func TestWitnessNamespaceStaysHiddenFromSpecEndpoints(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1), "live")

	for _, p := range []string{
		"/dedi/query/" + witnessNamespace,
		"/dedi/lookup/" + witnessNamespace,
		"/dedi/lookup/" + witnessNamespace + "/" + url.PathEscape(testTargetOrigin),
	} {
		getJSON(t, srv.URL+p, http.StatusNotFound)
	}
}

// The network view is where a reader arrives asking "is anyone checking this
// node". Evidence nobody can navigate to may as well be unpublished.
func TestNetworkViewLinksToTheVerdicts(t *testing.T) {
	srv, _, _ := testServer(t)

	m := getJSON(t, srv.URL+"/dedi/network", http.StatusOK)
	if m["data"].(map[string]any)["witness_verdicts_url"] != "/dedi/witness" {
		t.Errorf("network view does not point at the verdicts: %v", m["data"])
	}
}

// The store caps a page at 100 rows. A node witnessing more than that must not
// be told about a prefix and left to report it as the whole set: a monitor
// reading "12 of 12 consistent" cannot tell it was shown 12 of 130, and the
// targets it was not shown are precisely the ones nobody is watching.
func TestEveryTargetIsListedPastTheStorePageLimit(t *testing.T) {
	srv, s, _ := testServer(t)
	const n = 105
	for i := 0; i < n; i++ {
		seedVerdict(t, s, fmt.Sprintf("t%03d.example/log", i), soundVerdict(int64(i+1)), "live")
	}

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	data := m["data"].(map[string]any)
	if got := len(data["targets"].([]any)); got != n {
		t.Errorf("listed %d targets, want %d — the tail was silently dropped", got, n)
	}
	if data["total"].(float64) != n {
		t.Errorf("total = %v, want %d", data["total"], n)
	}
	if data["consistent"].(float64) != n {
		t.Errorf("consistent = %v, want %d", data["consistent"], n)
	}
}

// The verifier key is what makes a verdict re-checkable without us. It has to
// come from the registry, because for a delegated child the node's configured
// ring target is the wrong key and the child's own key lives in a delegation
// record this view cannot locate from the origin alone.
func TestTargetKeyComesFromTheWitnessRegistry(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "namespace", Namespace: witnessNamespace,
		PayloadRaw: []byte(`{"description":"witnessed"}`), CreatedBy: "witness"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "registry", Namespace: witnessNamespace,
		Registry:   "child.example/log",
		PayloadRaw: []byte(`{"target":"https://child.example/dedi","target_key":"child.example+abcd1234+AaaBBB"}`),
		CreatedBy:  "witness"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, store.AppendInput{EntryType: "record", Namespace: witnessNamespace,
		Registry: "child.example/log", RecordName: "checkpoint",
		PayloadRaw: []byte(soundVerdict(7)), State: "live", CreatedBy: "witness"}); err != nil {
		t.Fatal(err)
	}

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	got := m["data"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	if got["target_key"] != "child.example+abcd1234+AaaBBB" {
		t.Errorf("target_key = %v, want the key recorded on the registry", got["target_key"])
	}
}

// A target whose key this node does not hold says so, rather than omitting the
// field — "we cannot help you check this" and "we forgot" are different facts.
func TestUnknownTargetKeyIsStated(t *testing.T) {
	srv, s, _ := testServer(t)
	seedVerdict(t, s, testTargetOrigin, soundVerdict(1), "live")

	m := getJSON(t, srv.URL+"/dedi/witness", http.StatusOK)
	got := m["data"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	if got["target_key_known"] != false {
		t.Errorf("target_key_known = %v, want false", got["target_key_known"])
	}
}
