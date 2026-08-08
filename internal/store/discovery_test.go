package store

import (
	"context"
	"errors"
	"testing"
)

func seedParticipant(t *testing.T, s *Store, ns, name, payload, state string) {
	t.Helper()
	if _, err := s.Append(context.Background(), AppendInput{
		EntryType: "record", Namespace: ns, Registry: "subscribers", RecordName: name,
		PayloadRaw: []byte(payload), State: state, CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func discoveryFixture(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	for _, ns := range []string{"beckn", "rogue"} {
		seedRegistry(t, s, ns, "subscribers")
	}
	return s
}

func names(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.RecordName)
	}
	return out
}

func has(entries []Entry, name string) bool {
	for _, e := range entries {
		if e.RecordName == name {
			return true
		}
	}
	return false
}

// The read that did not exist: who serves this domain. FindBecknSubscriber
// could only answer for a subscriber_id the caller already knew.
func TestServingDomainFindsWhoeverDeclaresIt(t *testing.T) {
	s := discoveryFixture(t)
	seedParticipant(t, s, "beckn", "retail-1",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED","url":"https://a"}`, "live")
	seedParticipant(t, s, "beckn", "mobility-1",
		`{"subscriber_id":"b","domain":"mobility","status":"SUBSCRIBED","url":"https://b"}`, "live")

	got, err := s.ServingDomain(context.Background(), "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RecordName != "retail-1" {
		t.Fatalf("want only retail-1, got %v", names(got))
	}
}

// The spec permits a participant to declare one domain or several. A seed that
// used the array form must not silently become undiscoverable — it would look
// like a participant that simply is not on the network.
func TestServingDomainMatchesBothTheScalarAndTheArrayForm(t *testing.T) {
	s := discoveryFixture(t)
	seedParticipant(t, s, "beckn", "scalar",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "live")
	seedParticipant(t, s, "beckn", "array",
		`{"subscriber_id":"b","domain":["mobility","retail"],"status":"SUBSCRIBED"}`, "live")

	got, err := s.ServingDomain(context.Background(), "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !has(got, "scalar") || !has(got, "array") {
		t.Fatalf("both spellings should resolve, got %v", names(got))
	}
}

// This is what the read is *for*. Revocation already stopped a participant being
// verifiable; here it stops it being returned as a destination — a different
// protection, because the first stops a forged message being accepted and the
// second stops a real one being sent somewhere it should not go.
func TestARevokedParticipantStopsBeingReturned(t *testing.T) {
	s := discoveryFixture(t)
	ctx := context.Background()
	seedParticipant(t, s, "beckn", "bap-1",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "live")

	got, err := s.ServingDomain(ctx, "retail", []string{"beckn"}, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("setup: %v %v", names(got), err)
	}

	seedParticipant(t, s, "beckn", "bap-1",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "revoked")

	got, err = s.ServingDomain(ctx, "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a revoked participant is still being offered as a destination: %v", names(got))
	}
}

// A participant mid-onboarding or already unsubscribed is not somewhere to send
// business. Records declaring no status at all still resolve: many seeds predate
// the field, and dropping them would quietly take working participants off the
// network.
func TestSubscriberStatusGatesDiscovery(t *testing.T) {
	s := discoveryFixture(t)
	for name, status := range map[string]string{
		"initiated":    `"status":"INITIATED",`,
		"unsubscribed": `"status":"UNSUBSCRIBED",`,
		"invalid-ssl":  `"status":"INVALID_SSL",`,
	} {
		seedParticipant(t, s, "beckn", name,
			`{"subscriber_id":"`+name+`",`+status+`"domain":"retail"}`, "live")
	}
	seedParticipant(t, s, "beckn", "legacy", `{"subscriber_id":"l","domain":"retail"}`, "live")

	got, err := s.ServingDomain(context.Background(), "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RecordName != "legacy" {
		t.Fatalf("want only the status-less legacy record, got %v", names(got))
	}
}

// The allowlist matters more here than it does for lookup. A wildcard lookup is
// at least anchored to a subscriber_id the caller already believed in; a
// discovery caller has no such anchor, so without this anyone able to publish on
// this node could insert themselves as a destination for any domain and receive
// traffic meant for someone else.
func TestAnIneligibleNamespaceCannotAnswerDiscovery(t *testing.T) {
	s := discoveryFixture(t)
	seedParticipant(t, s, "beckn", "honest",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "live")
	seedParticipant(t, s, "rogue", "impostor",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "live")

	got, err := s.ServingDomain(context.Background(), "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if has(got, "impostor") {
		t.Fatalf("a namespace outside the allowlist answered a discovery query: %v", names(got))
	}
	if !has(got, "honest") {
		t.Fatalf("the eligible namespace stopped answering: %v", names(got))
	}

	// And an explicitly empty allowlist means nothing may answer, rather than
	// falling through to everything.
	got, err = s.ServingDomain(context.Background(), "retail", []string{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an empty allowlist returned %v", names(got))
	}
}

// Only the latest version of a record decides. An earlier live version must not
// keep a participant discoverable after it was revoked.
func TestOnlyTheLatestVersionOfARecordCounts(t *testing.T) {
	s := discoveryFixture(t)
	seedParticipant(t, s, "beckn", "moved",
		`{"subscriber_id":"a","domain":"retail","status":"SUBSCRIBED"}`, "live")
	seedParticipant(t, s, "beckn", "moved",
		`{"subscriber_id":"a","domain":"mobility","status":"SUBSCRIBED"}`, "live")

	retail, err := s.ServingDomain(context.Background(), "retail", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(retail) != 0 {
		t.Fatalf("a superseded version is still answering for its old domain: %v", names(retail))
	}
	mobility, err := s.ServingDomain(context.Background(), "mobility", []string{"beckn"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mobility) != 1 {
		t.Fatalf("the current version does not answer for its domain: %v", names(mobility))
	}
}

func TestServingDomainRejectsAnEmptyDomain(t *testing.T) {
	s := discoveryFixture(t)
	if _, err := s.ServingDomain(context.Background(), "  ", nil, 0); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("got %v, want ErrInvalidFilter", err)
	}
}
