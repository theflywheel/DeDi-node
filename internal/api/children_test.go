package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// parentServer boots a node that holds `ns` and can delegate under it. It
// returns the Server itself as well, because the delegation hooks — the peer
// monitor and OnDelegation — are the parts most worth asserting on.
func parentServer(t *testing.T, namespaces ...string) (*httptest.Server, *Server, ed25519.PrivateKey) {
	t.Helper()
	base, s, _ := testServer(t)
	base.Close()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// One key id per namespace, because Key.Authorizes is an exact match: a key
	// scoped to beckn cannot write under beckn.mobility, which is exactly why a
	// grandchild has to be minted by the child node rather than by this one.
	entries := make([]string, 0, len(namespaces))
	for i, ns := range namespaces {
		entries = append(entries, fmt.Sprintf("op-%d:%s:%s", i+1, ns, base64.StdEncoding.EncodeToString(pub)))
	}
	keys, err := publisher.ParseKeySet(strings.Join(entries, ","))
	if err != nil {
		t.Fatal(err)
	}
	skey, vkey, err := note.GenerateKey(rand.Reader, "parent.test")
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "parent.test/log", Interval: time.Hour},
		TTL: 300, VerifierKey: vkey, WildcardNamespaces: namespaces,
		Auth:    &publisher.Authenticator{Keys: keys},
		Network: &network.Monitor{},
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, api, priv
}

func mintOffer(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, ns, child string) (string, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(createChildRequest{Namespace: child, Label: "mobility", Provider: "env"})
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/"+ns+"/children", body, publisher.Precondition{})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	token, _ := out.Data["token"].(string)
	if token == "" {
		t.Fatalf("no token in response: %s", raw)
	}
	return token, out.Data
}

func enrol(t *testing.T, srv *httptest.Server, en delegation.Enrolment) *http.Response {
	t.Helper()
	body, _ := json.Marshal(en)
	resp, err := http.Post(srv.URL+"/enrol", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// Generated, not written out: the hash inside a verifier key is derived from
// the name and public key, so a hand-made one does not parse.
var childKey = func() string {
	_, vkey, err := note.GenerateKey(rand.Reader, "beckn.mobility/log")
	if err != nil {
		panic(err)
	}
	return vkey
}()

func childEnrolment(token string) delegation.Enrolment {
	return delegation.Enrolment{
		Namespace: "beckn.mobility", Token: token,
		Origin: "beckn.mobility/log", URL: "https://mobility.example",
		Key: childKey,
	}
}

func TestCreatingAChildMintsAnOfferAndPublishesItToTheLog(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	token, data := mintOffer(t, srv, priv, "beckn", "beckn.mobility")

	// The artifact must carry everything the child needs to come back.
	art, _ := data["artifact"].(map[string]any)
	content, _ := art["content"].(string)
	for _, want := range []string{token, "beckn.mobility", srv.URL} {
		if !bytes.Contains([]byte(content), []byte(want)) {
			t.Errorf("artifact does not carry %q:\n%s", want, content)
		}
	}

	// And the log holds the offer — with the hash, not the token.
	rec, err := api.currentDelegation(t.Context(), "beckn", "beckn.mobility")
	if err != nil {
		t.Fatalf("delegation not published to the log: %v", err)
	}
	if rec.State != delegation.StateOffered {
		t.Errorf("state = %q, want %q", rec.State, delegation.StateOffered)
	}
	if rec.TokenHash != delegation.HashToken(token) {
		t.Error("the published hash does not match the token that was issued")
	}
}

func TestEnrolmentRecordsTheChildAndStartsWatchingIt(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")

	var hooked delegation.Record
	api.OnDelegation = func(rec delegation.Record) { hooked = rec }

	resp := enrol(t, srv, childEnrolment(token))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("enrol: %d %s", resp.StatusCode, raw)
	}

	rec, err := api.currentDelegation(t.Context(), "beckn", "beckn.mobility")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != delegation.StateActive || rec.ChildOrigin != "beckn.mobility/log" {
		t.Fatalf("delegation not activated: %+v", rec)
	}
	if rec.ChildKey == "" {
		t.Error("the child's verifier key was not recorded, so nobody can check its checkpoints")
	}

	// Watching must begin immediately. A child that only appears after a
	// restart reads to the operator as an enrolment that failed.
	if got := api.Network.Snapshot(); len(got) != 1 || got[0].URL != "https://mobility.example" {
		t.Errorf("child was not added to the network view: %+v", got)
	}
	if hooked.ChildOrigin != "beckn.mobility/log" {
		t.Errorf("OnDelegation not called with the enrolled child: %+v", hooked)
	}
}

func TestASpentTokenCannotBeRedeemedTwice(t *testing.T) {
	// The takeover case: a token that leaks after the child has enrolled must
	// not let anyone else become the holder of that namespace.
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")

	first := enrol(t, srv, childEnrolment(token))
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first enrolment: %d", first.StatusCode)
	}

	impostor := childEnrolment(token)
	impostor.Origin, impostor.URL = "evil/log", "https://evil.example"
	second := enrol(t, srv, impostor)
	defer second.Body.Close()
	if second.StatusCode == http.StatusOK {
		t.Fatal("a second claimant redeemed a spent token and took the namespace")
	}
}

func TestAWrongTokenIsRejectedAndWritesNothing(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	mintOffer(t, srv, priv, "beckn", "beckn.mobility")

	en := childEnrolment("wrong-token")
	resp := enrol(t, srv, en)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	rec, err := api.currentDelegation(t.Context(), "beckn", "beckn.mobility")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != delegation.StateOffered {
		t.Errorf("a rejected enrolment changed the record to %q", rec.State)
	}
}

func TestEnrolmentForANamespaceThisNodeDoesNotHoldIsRefused(t *testing.T) {
	// Same answer as a bad token, so a caller cannot enumerate which
	// namespaces have offers outstanding.
	srv, _, _ := parentServer(t, "beckn")
	en := childEnrolment("anything")
	en.Namespace = "someoneelse.mobility"
	resp := enrol(t, srv, en)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAnActiveDelegationCannotBeSilentlyReIssued(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()

	body, _ := json.Marshal(createChildRequest{Namespace: "beckn.mobility", Provider: "env"})
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/beckn/children", body, publisher.Precondition{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-issuing over a live child: %d, want 409", resp.StatusCode)
	}
}

func TestDelegatingOutsideYourOwnNamespaceIsRefused(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	body, _ := json.Marshal(createChildRequest{Namespace: "onix.mobility", Provider: "env"})
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/beckn/children", body, publisher.Precondition{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestMintingAChildRequiresASignature(t *testing.T) {
	// Granting a slice of your namespace away is at least as consequential as
	// publishing in it, and must not be reachable unauthenticated.
	srv, _, _ := parentServer(t, "beckn")
	body, _ := json.Marshal(createChildRequest{Namespace: "beckn.mobility"})
	resp := signedDo(t, srv, nil, http.MethodPost, "/admin/namespaces/beckn/children", body, publisher.Precondition{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDelegationsAreReadablePublicly(t *testing.T) {
	// Who holds a namespace is the question relying parties need answered, so
	// it must not require a credential.
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()

	resp, err := http.Get(srv.URL + "/dedi/delegations/beckn")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Data struct {
			Children []delegation.Record `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if len(out.Data.Children) != 1 {
		t.Fatalf("want 1 child, got %d: %s", len(out.Data.Children), raw)
	}
	child := out.Data.Children[0]
	if child.ChildKey == "" {
		t.Error("the child's key is not published, so its checkpoints cannot be verified independently")
	}
	// And the public listing must not leak an outstanding offer's hash, which
	// would let a reader confirm a guessed token offline.
	if bytes.Contains(raw, []byte("token_hash")) {
		t.Errorf("the public delegation listing carries a token hash:\n%s", raw)
	}
}

func TestTheDelegationRecordIsProvableLikeAnyOtherEntry(t *testing.T) {
	// The reason delegation lives in the log rather than a side table: a
	// relying party can demand proof, not the operator's word.
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()

	e, err := api.Store.Resolve(t.Context(), "record", "beckn", delegation.Registry, "beckn.mobility", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	size, err := api.Store.TreeSize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Store.ProveInclusion(t.Context(), size, e.Seq); err != nil {
		t.Fatalf("the delegation is not provable: %v", err)
	}
	var _ store.Entry = e
}

func revokeDelegation(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, ns, child, reason string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(revokeChildRequest{Reason: reason})
	return signedDo(t, srv, priv, http.MethodPost,
		"/admin/namespaces/"+ns+"/children/"+child+"/revoke", body, publisher.Precondition{})
}

func TestRevokingADelegationPublishesTheWithdrawalAndFreesTheNamespace(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()

	resp := revokeDelegation(t, srv, priv, "beckn", "beckn.mobility", "operator error")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d %s", resp.StatusCode, raw)
	}

	// The withdrawal has to be visible on the public read surface. Someone
	// holding a signature from this child needs to see that the grant behind it
	// is gone, and a revocation only the operator can see does not do that.
	pub, err := http.Get(srv.URL + "/dedi/delegations/beckn")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Body.Close()
	var out struct {
		Data struct {
			Children []map[string]any `json:"children"`
		} `json:"data"`
	}
	if err := json.NewDecoder(pub.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Children) != 1 {
		t.Fatalf("children = %v", out.Data.Children)
	}
	kid := out.Data.Children[0]
	if kid["state"] != delegation.StateRevoked {
		t.Errorf("state = %v, want revoked", kid["state"])
	}
	if kid["revoked_at"] == "" || kid["reason"] != "operator error" {
		t.Errorf("withdrawal not described: %v", kid)
	}
	// And the identity of who held it stays readable, because that is what an
	// audit of a past signature from the child depends on.
	if kid["child_key"] == "" {
		t.Error("the child's key was erased by revocation")
	}

	// Re-minting is refused over a live delegation and permitted over a
	// revoked one — otherwise revocation frees nothing and the operator is
	// still stuck.
	if _, d := mintOffer(t, srv, priv, "beckn", "beckn.mobility"); d["token"] == "" {
		t.Error("could not re-delegate a revoked namespace")
	}
}

func TestARevokedOfferCannotBeEnrolledAgainstEvenWithTheRightToken(t *testing.T) {
	// The offer case, not the enrolled-child case: whoever holds the token
	// still holds a valid secret, and only the state stands between them and
	// the namespace.
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	revokeDelegation(t, srv, priv, "beckn", "beckn.mobility", "minted by mistake").Body.Close()

	resp := enrol(t, srv, childEnrolment(token))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enrolling against a revoked offer: %d, want 401", resp.StatusCode)
	}
}

func TestRevokingRequiresASignature(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()

	resp := revokeDelegation(t, srv, nil, "beckn", "beckn.mobility", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — an unsigned caller must not be able to strip a child of its namespace", resp.StatusCode)
	}
}

func TestRevokingSomethingThatWasNeverDelegatedIs404(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	resp := revokeDelegation(t, srv, priv, "beckn", "beckn.mobility", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestADelegationCannotBeRevokedTwiceOverTheAPI(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	revokeDelegation(t, srv, priv, "beckn", "beckn.mobility", "").Body.Close()

	resp := revokeDelegation(t, srv, priv, "beckn", "beckn.mobility", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// witnessVerdict writes a verdict for a child the way internal/witness does, so
// the join being tested runs against the real record shape rather than a
// fixture only this test agrees with.
func witnessVerdict(t *testing.T, s *store.Store, origin string, size int64, consistencyOK bool) {
	t.Helper()
	ctx := t.Context()
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: witnessNamespace, PayloadRaw: []byte(`{"description":"witness"}`)},
		{EntryType: "registry", Namespace: witnessNamespace, Registry: origin, PayloadRaw: []byte(`{"description":"verdicts"}`)},
	} {
		if _, err := s.Resolve(ctx, in.EntryType, in.Namespace, in.Registry, "", nil, nil); errors.Is(err, store.ErrNotFound) {
			in.CreatedBy = "test"
			if _, err := s.Append(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"target": "https://mobility.example/dedi", "size": size,
		"root": "cm9vdA", "consistency_ok": consistencyOK,
	})
	state := "live"
	if !consistencyOK {
		state = "revoked"
	}
	if _, err := s.Append(ctx, store.AppendInput{
		EntryType: "record", Namespace: witnessNamespace, Registry: origin,
		RecordName: "checkpoint", PayloadRaw: payload, State: state, CreatedBy: "witness",
	}); err != nil {
		t.Fatal(err)
	}
}

func childWitnessFrom(t *testing.T, srv *httptest.Server) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + "/dedi/delegations/beckn")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Children []map[string]any `json:"children"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Children) != 1 {
		t.Fatalf("children = %v", out.Data.Children)
	}
	wit, _ := out.Data.Children[0]["witness"].(map[string]any)
	if wit == nil {
		t.Fatalf("no witness block on an active child: %v", out.Data.Children[0])
	}
	return wit
}

func TestTheDelegationListCarriesWhatWasActuallyVerified(t *testing.T) {
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	witnessVerdict(t, api.Store, "beckn.mobility/log", 42, true)

	api.ChildWitnessHealth = func(string) (WitnessState, bool) {
		return WitnessState{
			LastAttemptAt: time.Now(), LastSuccessAt: time.Now(),
			Attempts: 5, Interval: time.Minute,
		}, true
	}
	wit := childWitnessFrom(t, srv)
	if wit["size"] != float64(42) || wit["consistency_ok"] != true {
		t.Errorf("verdict not carried: %v", wit)
	}
	health, _ := wit["health"].(map[string]any)
	if health["stale"] != false || health["checking"] != true {
		t.Errorf("a live witness reported as not checking: %v", health)
	}
}

func TestAFailedConsistencyCheckIsVisibleOnTheChild(t *testing.T) {
	// The alarm this whole table exists to raise: the child's log stopped being
	// append-only.
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	witnessVerdict(t, api.Store, "beckn.mobility/log", 7, false)

	if wit := childWitnessFrom(t, srv); wit["consistency_ok"] != false {
		t.Errorf("a failed consistency check did not surface: %v", wit)
	}
}

func TestAStalledChildWitnessDoesNotReadAsAPassingCheck(t *testing.T) {
	// The failure this join exists to make visible. A verdict is rewritten only
	// when the child's tree changes, so a loop that has been failing for hours
	// still shows its last consistency_ok. Without the health block beside it,
	// that is indistinguishable from a check that ran a second ago.
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	witnessVerdict(t, api.Store, "beckn.mobility/log", 42, true)

	api.ChildWitnessHealth = func(string) (WitnessState, bool) {
		return WitnessState{
			LastAttemptAt: time.Now(),
			LastSuccessAt: time.Now().Add(-time.Hour), // last worked an hour ago
			LastError:     "dial tcp: connection refused",
			Attempts:      60, Failures: 59, Interval: time.Minute,
		}, true
	}
	wit := childWitnessFrom(t, srv)
	if wit["consistency_ok"] != true {
		t.Fatal("precondition: the frozen verdict should still read ok")
	}
	health, _ := wit["health"].(map[string]any)
	if health["stale"] != true {
		t.Errorf("a witness failing for an hour is not reported stale: %v", health)
	}
	if health["last_error"] == "" {
		t.Errorf("no reason given for the stall: %v", health)
	}
}

func TestAnActiveChildWithNoWitnessLoopIsNotReportedHealthy(t *testing.T) {
	// A child enrolled before a restart that Resume did not pick up. Nothing is
	// wrong with the verdict; it has simply stopped being refreshed, and the
	// only place that shows is here.
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	witnessVerdict(t, api.Store, "beckn.mobility/log", 42, true)

	api.ChildWitnessHealth = func(string) (WitnessState, bool) { return WitnessState{}, false }
	health, _ := childWitnessFrom(t, srv)["health"].(map[string]any)
	if health["stale"] != true || health["checking"] != false {
		t.Errorf("a child with no witness loop reported as fine: %v", health)
	}
}

func TestAnUnredeemedOfferSaysWhetherItIsStillRedeemable(t *testing.T) {
	srv, _, priv := parentServer(t, "beckn")
	mintOffer(t, srv, priv, "beckn", "beckn.mobility")

	resp, err := http.Get(srv.URL + "/dedi/delegations/beckn")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Data struct {
			Children []map[string]any `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	kid := out.Data.Children[0]
	if kid["expires_at"] == nil || kid["expired"] != false {
		t.Errorf("a fresh offer does not report its life: %v", kid)
	}
	// The expiry is public; the thing it guards is not. This surface is
	// unauthenticated, so a token hash leaking here would hand every reader a
	// target to grind against an offer that is still live.
	if bytes.Contains(raw, []byte("token")) {
		t.Errorf("the delegation list leaks token material:\n%s", raw)
	}
}

func TestReMintingOverAnUnredeemedOfferInvalidatesTheOldToken(t *testing.T) {
	// Re-minting is refused over an *active* delegation, because that would be
	// a takeover of the child. Over an unredeemed offer it is the supported way
	// out of a stale one — and the old token has to die with it, or the
	// operator has quietly doubled the number of credentials that can claim the
	// namespace instead of replacing one.
	srv, _, priv := parentServer(t, "beckn")
	stale, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	fresh, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	if stale == fresh {
		t.Fatal("re-minting returned the same token")
	}

	old := enrol(t, srv, childEnrolment(stale))
	old.Body.Close()
	if old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the superseded token still enrols: %d, want 401", old.StatusCode)
	}
	now := enrol(t, srv, childEnrolment(fresh))
	defer now.Body.Close()
	if now.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(now.Body)
		t.Fatalf("the fresh token does not enrol: %d %s", now.StatusCode, raw)
	}
}

// signedAs is signedDo with a chosen key id, needed once a node holds more than
// one publisher key — which is what a node running a delegated namespace does.
func signedAs(t *testing.T, srv *httptest.Server, priv ed25519.PrivateKey, kid, method, path string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	req.Header.Set(publisher.HeaderKeyID, kid)
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature,
		publisher.Sign(priv, method, path, body, publisher.Precondition{}, now))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAChildCanDelegateAGrandchildButItsParentCannot(t *testing.T) {
	// The one-level rule is enforced relative to whoever is granting, so a
	// chain is allowed to grow — but only one link at a time, and only by the
	// holder of the link above. Nothing tested this, and "nothing forbids it"
	// is not the same as "it works".
	srv, _, priv := parentServer(t, "beckn", "beckn.mobility")

	// beckn may not reach past its own child, even holding both keys: the
	// route's namespace is beckn, and beckn.mobility.metro is two levels below.
	skip := signedAs(t, srv, priv, "op-1", http.MethodPost, "/admin/namespaces/beckn/children",
		mustJSON(createChildRequest{Namespace: "beckn.mobility.metro", Provider: "env"}))
	skip.Body.Close()
	if skip.StatusCode != http.StatusBadRequest {
		t.Fatalf("beckn delegated a grandchild directly: %d, want 400", skip.StatusCode)
	}

	// The holder of beckn.mobility may, because for it that is one level down.
	resp := signedAs(t, srv, priv, "op-2", http.MethodPost, "/admin/namespaces/beckn.mobility/children",
		mustJSON(createChildRequest{Namespace: "beckn.mobility.metro", Provider: "env"}))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a child could not delegate a grandchild: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	token, _ := out.Data["token"].(string)
	if token == "" {
		t.Fatalf("no token minted: %s", raw)
	}

	// And the grandchild enrols against the level above it, not against the
	// root — parentNamespaceOf has to pick beckn.mobility out of a node that
	// also holds beckn.
	_, gvkey, err := note.GenerateKey(rand.Reader, "beckn.mobility.metro/log")
	if err != nil {
		t.Fatal(err)
	}
	en := enrol(t, srv, delegation.Enrolment{
		Namespace: "beckn.mobility.metro", Token: token,
		Origin: "beckn.mobility.metro/log", URL: "https://metro.example", Key: gvkey,
	})
	defer en.Body.Close()
	if en.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(en.Body)
		t.Fatalf("grandchild enrolment: %d %s", en.StatusCode, raw)
	}

	// The chain is readable a link at a time: each level lists only its own
	// children, which is what a tree walk has to follow.
	for _, tc := range []struct{ ns, want string }{
		{"beckn", ""}, // beckn delegated nothing in this test
		{"beckn.mobility", "beckn.mobility.metro"},
	} {
		resp, err := http.Get(srv.URL + "/dedi/delegations/" + tc.ns)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Data struct {
				Children []map[string]any `json:"children"`
			} `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if tc.want == "" {
			if len(got.Data.Children) != 0 {
				t.Errorf("%s lists children it did not grant: %v", tc.ns, got.Data.Children)
			}
			continue
		}
		if len(got.Data.Children) != 1 || got.Data.Children[0]["namespace"] != tc.want {
			t.Errorf("%s children = %v, want %s", tc.ns, got.Data.Children, tc.want)
		}
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestReachabilityIsReportedSeparatelyFromTheVerdict(t *testing.T) {
	// The two must not merge. A node being up proves nothing about its log, and
	// a node briefly down disproves nothing — letting uptime read as
	// verification is the confusion this design spends its effort avoiding.
	srv, api, priv := parentServer(t, "beckn")
	token, _ := mintOffer(t, srv, priv, "beckn", "beckn.mobility")
	enrol(t, srv, childEnrolment(token)).Body.Close()
	witnessVerdict(t, api.Store, "beckn.mobility/log", 3, true)

	api.Network.Observe(network.Status{
		Name: "beckn.mobility/log", URL: "https://mobility.example",
		Reachable: false, Error: "dial tcp: connection refused",
		CheckedAt: time.Now().UTC(),
	})

	resp, err := http.Get(srv.URL + "/dedi/delegations/beckn")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Children []map[string]any `json:"children"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	kid := out.Data.Children[0]
	node, _ := kid["node"].(map[string]any)
	if node == nil || node["reachable"] != false || node["error"] == "" {
		t.Fatalf("the child's reachability is not reported: %v", kid)
	}
	// And the verdict is untouched by the node being down. The log it already
	// verified did not become unverified because the host stopped answering.
	wit, _ := kid["witness"].(map[string]any)
	if wit["consistency_ok"] != true {
		t.Errorf("an unreachable node changed what had been verified: %v", wit)
	}
}
