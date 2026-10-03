package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/api"
	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
	"github.com/theflywheel/DeDi-node/internal/testdb"
	"golang.org/x/mod/sumdb/note"
)

// A subscription made on a standalone node must actually be stored.
//
// serve wraps the store in notifyingAppender on every node, clustered or not.
// Its Webhook used to return nil when the wrapped writer was the bare store, so
// the API acknowledged the change, wrote nothing, and its read-back answered
// 500. The API's own tests never caught it because they build a server without
// that wrapper, which falls back to the store directly. This one is wired the
// way serve wires an unclustered node.
func TestASubscriptionOnAStandaloneNodeIsStored(t *testing.T) {
	const ns, reg = "demo", "people"
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: ns, PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "seed"},
		{EntryType: "registry", Namespace: ns, Registry: reg, PayloadRaw: []byte(`{"description":"r","schema":{"type":"object"}}`), CreatedBy: "seed"},
	} {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := publisher.ParseKeySet("op-1:" + ns + ":" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	skey, _, _ := note.GenerateKey(rand.Reader, "wiring.test")
	srv := httptest.NewServer((&api.Server{
		Store: s, CP: &checkpoint.Checkpointer{Store: s, SKey: skey, Origin: "wiring.test/log", Interval: time.Hour},
		TTL: 300, WildcardNamespaces: []string{ns},
		AllowPrivateWebhookTargets: true, // the target is never called; it only has to be accepted
		Auth:                       &publisher.Authenticator{Keys: keys},
		Writer:                     notifyingAppender{inner: s, notify: func() {}}, // as serve builds it with no cluster
	}).Handler())
	t.Cleanup(srv.Close)

	path := "/admin/namespaces/" + ns + "/registries/" + reg + "/subscriptions"
	body := []byte(`{"target_url":"http://127.0.0.1:9/hook"}`)
	now := time.Now().UTC()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	req.Header.Set(publisher.HeaderKeyID, "op-1")
	req.Header.Set(publisher.HeaderTimestamp, now.Format(time.RFC3339))
	req.Header.Set(publisher.HeaderSignature, publisher.Sign(priv, http.MethodPost, path, body, publisher.Precondition{}, now))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("subscribing on a standalone node: %d %s", resp.StatusCode, msg)
	}
	subs, err := s.AllSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("the node acknowledged the subscription but stored %d", len(subs))
	}
}

// A writer that can apply neither must refuse, not report success.
func TestASubscriptionChangeTheWriterCannotApplyIsRefused(t *testing.T) {
	err := notifyingAppender{inner: appendOnly{}, notify: func() {}}.Webhook(context.Background(),
		store.WebhookCommand{Op: store.WebhookCreate, ID: "x"})
	if err == nil {
		t.Fatal("a subscription change that went nowhere was reported as done")
	}
}

type appendOnly struct{}

func (appendOnly) Append(context.Context, store.AppendInput) (store.Entry, error) {
	return store.Entry{}, nil
}

// On a cluster the wrapped writer is the Raft proposer, which replicates the
// change. The wrapper must hand it over rather than apply it to its own store,
// or the subscription exists on the leader alone.
func TestASubscriptionChangeIsForwardedToAWriterThatReplicates(t *testing.T) {
	inner := &replicating{}
	cmd := store.WebhookCommand{Op: store.WebhookCreate, ID: "s1"}
	if err := (notifyingAppender{inner: inner, notify: func() {}}).Webhook(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if len(inner.got) != 1 || inner.got[0].ID != "s1" {
		t.Fatalf("the change was not forwarded to the replicating writer: %v", inner.got)
	}
}

type replicating struct{ got []store.WebhookCommand }

func (*replicating) Append(context.Context, store.AppendInput) (store.Entry, error) {
	return store.Entry{}, nil
}

func (r *replicating) Webhook(_ context.Context, c store.WebhookCommand) error {
	r.got = append(r.got, c)
	return nil
}
