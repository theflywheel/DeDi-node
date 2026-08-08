package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedRegistry(t *testing.T, s *Store, ns, reg string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Append(ctx, AppendInput{
		EntryType: "namespace", Namespace: ns,
		PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed namespace: %v", err)
	}
	if _, err := s.Append(ctx, AppendInput{
		EntryType: "registry", Namespace: ns, Registry: reg,
		PayloadRaw: []byte(`{}`), CreatedBy: "test",
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

func seedRecord(t *testing.T, s *Store, ns, reg, name string) Entry {
	t.Helper()
	e, err := s.Append(context.Background(), AppendInput{
		EntryType: "record", Namespace: ns, Registry: reg, RecordName: name,
		PayloadRaw: []byte(`{"k":"v"}`), CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("seed record %s: %v", name, err)
	}
	return e
}

func subscribe(t *testing.T, s *Store, id, ns, reg string) WebhookSubscription {
	t.Helper()
	ctx := context.Background()
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookCreate, ID: id, Namespace: ns, Registry: reg,
		TargetURL: "https://consumer.example/hook", At: time.Now(),
	}); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	sub, err := s.Subscription(ctx, id)
	if err != nil {
		t.Fatalf("read back subscription: %v", err)
	}
	return sub
}

// A subscription registered today must not replay the whole log at a consumer
// that only asked to hear what happens next. The cursor therefore starts at the
// current head rather than at -1, and the entries that already existed are
// never pending.
func TestANewSubscriptionStartsAtTheHeadNotTheBeginning(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	seedRecord(t, s, "beckn", "subscribers", "old-one")
	seedRecord(t, s, "beckn", "subscribers", "old-two")

	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")

	pending, err := s.PendingFor(ctx, sub, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("a fresh subscription has %d entries pending; it should have none — "+
			"the consumer would be sent history it never asked for", len(pending))
	}

	fresh := seedRecord(t, s, "beckn", "subscribers", "new-one")
	pending, err = s.PendingFor(ctx, sub, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Seq != fresh.Seq {
		t.Fatalf("want only seq %d pending, got %v", fresh.Seq, pending)
	}
}

// A consumer subscribes to one registry. Traffic in another registry — or a
// namespace's own metadata versions — is not what it asked to be told about,
// and delivering it would train the consumer to ignore pushes.
func TestPendingIsScopedToTheSubscribedRegistry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	seedRegistry(t, s, "beckn", "other")
	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")

	seedRecord(t, s, "beckn", "other", "not-mine")
	mine := seedRecord(t, s, "beckn", "subscribers", "mine")

	pending, err := s.PendingFor(ctx, sub, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Seq != mine.Seq {
		t.Fatalf("want only the subscribed registry's entry (seq %d), got %v", mine.Seq, pending)
	}
}

// Delivery is at-least-once across a failover: a deposed leader can confirm a
// delivery the new leader has already moved past. That confirmation must not
// walk the cursor backwards, or every entry after it is sent a second time —
// and on a busy registry, a third and a fourth.
func TestALateConfirmationCannotRewindTheCursor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")
	a := seedRecord(t, s, "beckn", "subscribers", "a")
	b := seedRecord(t, s, "beckn", "subscribers", "b")

	for _, seq := range []int64{b.Seq, a.Seq} {
		if err := s.ApplyWebhook(ctx, WebhookCommand{
			Op: WebhookAdvance, ID: sub.ID, Seq: seq, At: time.Now(),
		}); err != nil {
			t.Fatalf("advance to %d: %v", seq, err)
		}
	}
	got, err := s.Subscription(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CursorSeq != b.Seq {
		t.Fatalf("cursor is at %d after a stale confirmation for %d; want it held at %d",
			got.CursorSeq, a.Seq, b.Seq)
	}
}

// One consumer that rejects one entry must not wedge its own subscription. If
// the cursor could not move past a dead letter, the next revocation — the whole
// point of the subsystem — would queue behind a payload nobody is going to fix.
func TestADeadLetterLetsTheCursorMoveOn(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")
	bad := seedRecord(t, s, "beckn", "subscribers", "poison")
	next := seedRecord(t, s, "beckn", "subscribers", "the-revocation")

	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookDeadLetter, ID: sub.ID, Seq: bad.Seq, Attempts: 5,
		LastError: "consumer answered 400", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	moved, err := s.Subscription(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingFor(ctx, moved, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Seq != next.Seq {
		t.Fatalf("after dead-lettering seq %d the next entry (seq %d) should be pending, got %v",
			bad.Seq, next.Seq, pending)
	}

	// And it is recorded rather than silently dropped: what a consumer was
	// never told is exactly what an operator needs to be able to see.
	deads, err := s.DeadLetters(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deads) != 1 || deads[0].Seq != bad.Seq || deads[0].LastError == "" {
		t.Fatalf("want the failure recorded against seq %d, got %+v", bad.Seq, deads)
	}
}

// A retired subscription stops being delivered to, but its record of what was
// never delivered survives it.
func TestDeletingASubscriptionKeepsItsDeadLetters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")
	bad := seedRecord(t, s, "beckn", "subscribers", "poison")
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookDeadLetter, ID: sub.ID, Seq: bad.Seq, Attempts: 5,
		LastError: "gone", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookDelete, ID: sub.ID, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	active, err := s.ActiveSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("a deleted subscription is still being delivered to: %+v", active)
	}
	deads, err := s.DeadLetters(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deads) != 1 {
		t.Fatalf("want the dead letter to outlive the subscription, got %+v", deads)
	}
}

// The exactly-once discipline from applied.go applies here too, and for a
// sharper reason than appends: a replayed create would resurrect a
// subscription the operator had deleted and resume pushing to a target they
// had deliberately retired. Unlike a publisher write, nothing about a webhook
// command carries a precondition that would reject the replay on its own.
func TestAReplayedWebhookCommandIsNotAppliedTwice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")

	create := WebhookCommand{
		Op: WebhookCreate, ID: "sub-1", Namespace: "beckn", Registry: "subscribers",
		TargetURL: "https://consumer.example/hook", At: time.Now(),
	}
	if skipped, err := s.ApplyWebhookReplicated(ctx, 1, create); err != nil || skipped {
		t.Fatalf("first apply: skipped=%v err=%v", skipped, err)
	}
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookDelete, ID: "sub-1", At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// The replica restarts and Raft replays index 1.
	skipped, err := s.ApplyWebhookReplicated(ctx, 1, create)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !skipped {
		t.Fatal("index 1 was applied a second time")
	}
	got, err := s.Subscription(ctx, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "deleted" {
		t.Fatalf("the replay resurrected a deleted subscription: state is %q; "+
			"the node would resume pushing to a target the operator retired", got.State)
	}
}

// A snapshot that forgot the subscriptions would restore a replica that has
// silently stopped notifying anyone — the same failure as keeping them local,
// reached by a different route.
func TestRestoreBringsBackSubscriptionsAndTheirCursors(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seedRegistry(t, s, "beckn", "subscribers")
	sub := subscribe(t, s, "sub-1", "beckn", "subscribers")
	e := seedRecord(t, s, "beckn", "subscribers", "a")
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookAdvance, ID: sub.ID, Seq: e.Seq, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	subs, err := s.AllSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deads, err := s.AllDeadLetters(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RestoreWebhooks(ctx, subs, deads); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := s.Subscription(ctx, sub.ID)
	if err != nil {
		t.Fatalf("the subscription did not survive the restore: %v", err)
	}
	if got.CursorSeq != e.Seq {
		t.Fatalf("cursor came back at %d, want %d — a restored replica would re-send "+
			"everything the old one had already delivered", got.CursorSeq, e.Seq)
	}
}

// Malformed commands are deterministic rejections: every replica must reach the
// same verdict, so they are classified as ErrInvalidWrite rather than as node
// failures that would halt a replica.
func TestMalformedWebhookCommandsAreDeterministicRejections(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for name, cmd := range map[string]WebhookCommand{
		"no id":      {Op: WebhookCreate, Namespace: "beckn", Registry: "r", TargetURL: "https://x", At: time.Now()},
		"no target":  {Op: WebhookCreate, ID: "a", Namespace: "beckn", Registry: "r", At: time.Now()},
		"unknown op": {Op: WebhookOp("drop-table"), ID: "a", At: time.Now()},
	} {
		if err := s.ApplyWebhook(ctx, cmd); !errors.Is(err, ErrInvalidWrite) {
			t.Errorf("%s: got %v, want ErrInvalidWrite", name, err)
		}
	}
	if err := s.ApplyWebhook(ctx, WebhookCommand{
		Op: WebhookAdvance, ID: "never-created", Seq: 1, At: time.Now(),
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("advancing an unknown subscription: got %v, want ErrNotFound", err)
	}
	// The replicated path must not fill in a missing clock the way the local
	// one does: three replicas reading three clocks would write three
	// different rows for the same command.
	if _, err := s.ApplyWebhookReplicated(ctx, 1, WebhookCommand{
		Op: WebhookCreate, ID: "a", Namespace: "beckn", Registry: "r", TargetURL: "https://x",
	}); !errors.Is(err, ErrInvalidWrite) {
		t.Errorf("a replicated command with no proposer timestamp: got %v, want ErrInvalidWrite", err)
	}
}
