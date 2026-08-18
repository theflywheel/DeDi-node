package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// A refusal that has already written is not a refusal.
//
// Validation of the rendered spec used to run AFTER the delegation offer was
// appended, so a request the endpoint rejected still left a StateOffered record
// in the public _delegations registry — with a token never handed back, so
// nothing could ever redeem it. That is precisely the unredeemable offer this
// endpoint was changed to stop creating, reintroduced by the fix for it.
//
// The log is append-only, so this is not a tidiness point: the false statement
// cannot be withdrawn.
func TestARejectedChildLeavesNoOfferBehind(t *testing.T) {
	srv, s, priv := writeServer(t, "flywheel")
	ctx := context.Background()

	// A witness target with no verifier key. The daemon would start a witness
	// loop that fails on every run, so Spec.Validate refuses it.
	body := []byte(`{"namespace":"flywheel.mobility","witness_target_url":"https://b.example/dedi"}`)
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/flywheel/children", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	if _, err := s.Resolve(ctx, "record", "flywheel", delegation.Registry,
		"flywheel.mobility", nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a rejected request left a delegation offer in the log (resolve: %v) — "+
			"an append-only log cannot take that back", err)
	}
}

// The converse, so the test above cannot pass because the endpoint is simply
// broken: a complete request still mints an offer.
func TestAnAcceptedChildStillMintsItsOffer(t *testing.T) {
	srv, s, priv := writeServer(t, "flywheel")
	ctx := context.Background()

	body := []byte(`{"namespace":"flywheel.mobility"}`)
	resp := signedDo(t, srv, priv, http.MethodPost, "/admin/namespaces/flywheel/children", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := s.Resolve(ctx, "record", "flywheel", delegation.Registry,
		"flywheel.mobility", nil, nil); err != nil {
		t.Errorf("an accepted request minted no offer: %v", err)
	}
}
