package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// The webhook subscription plane.
//
// A subscription says "tell me when this registry changes". Registering one is
// a publisher write, signed and scoped to the namespace, for the same reason
// minting a child is: it is a standing instruction the node will act on, and it
// points the node's own outbound requests at an address the caller chose.
//
// That last part is why this file spends more code on the target URL than on
// anything else. Every other URL in this system is one a caller fetches. This
// one *the node* fetches, from inside whatever network the operator deployed it
// in, on a schedule, with retries. An unchecked target is a request forgery
// primitive pointed at the operator's own infrastructure — and on the cloud
// platforms this node is meant to be deployed to, the link-local metadata
// address hands out credentials to anyone who can make the instance ask for
// them.

// Webhooker replicates a change to the subscription table. In a cluster this is
// the Raft proposer; standalone it is the store.
type Webhooker interface {
	Webhook(ctx context.Context, c store.WebhookCommand) error
}

// storeWebhooker adapts the store to the same interface, for an unreplicated
// node. It exists so the handlers have exactly one path rather than a branch on
// whether this deployment happens to be clustered.
type storeWebhooker struct{ s *store.Store }

func (w storeWebhooker) Webhook(ctx context.Context, c store.WebhookCommand) error {
	return w.s.ApplyWebhook(ctx, c)
}

func (s *Server) webhooker() Webhooker {
	if wh, ok := s.Writer.(Webhooker); ok && s.Writer != nil {
		return wh
	}
	return storeWebhooker{s.Store}
}

type subscriptionRequest struct {
	TargetURL string `json:"target_url"`
}

type subscriptionDTO struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Registry  string `json:"registry"`
	TargetURL string `json:"target_url"`
	State     string `json:"state"`
	CursorSeq int64  `json:"cursor_seq"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func subscriptionView(sub store.WebhookSubscription) subscriptionDTO {
	return subscriptionDTO{
		ID: sub.ID, Namespace: sub.Namespace, Registry: sub.Registry,
		TargetURL: sub.TargetURL, State: sub.State, CursorSeq: sub.CursorSeq,
		CreatedAt: fmtTime(sub.CreatedAt), UpdatedAt: fmtTime(sub.UpdatedAt),
	}
}

// createSubscription registers a target to be told when this registry changes.
func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	if _, ok := scoped(w, r); !ok {
		return
	}
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")

	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "body must be JSON of the form {\"target_url\": \"https://...\"}")
		return
	}
	target, err := s.checkWebhookTarget(r.Context(), req.TargetURL)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	// The registry must exist. Subscribing to one that does not is always a
	// typo, and accepting it would leave a subscription that never fires and
	// looks, from the console, exactly like one that is working.
	if _, err := s.Store.Resolve(r.Context(), "registry", ns, reg, "", nil, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "registry")
			return
		}
		internal(w, err)
		return
	}

	id, err := newSubscriptionID()
	if err != nil {
		internal(w, err)
		return
	}
	cmd := store.WebhookCommand{
		Op: store.WebhookCreate, ID: id, Namespace: ns, Registry: reg,
		TargetURL: target, At: time.Now().UTC(),
	}
	if err := s.webhooker().Webhook(r.Context(), cmd); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	sub, err := s.Store.Subscription(r.Context(), id)
	if err != nil {
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, envelope{
		Message: "Subscription created successfully", Data: subscriptionView(sub),
	})
}

// listSubscriptions shows the operator what this node has been told to notify.
//
// Behind the write plane's authentication rather than public: a delegation is
// published because relying parties need it, but who this node pushes to is the
// operator's own integration wiring, and the target URLs are internal
// addresses more often than not.
func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := scoped(w, r); !ok {
		return
	}
	ns := r.PathValue("namespace")
	all, err := s.Store.AllSubscriptions(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	subs := make([]subscriptionDTO, 0, len(all))
	for _, sub := range all {
		if sub.Namespace != ns {
			continue
		}
		subs = append(subs, subscriptionView(sub))
	}
	ok(w, "Subscriptions retrieved successfully", map[string]any{
		"namespace":     ns,
		"subscriptions": subs,
	})
}

// deleteSubscription retires one. The row survives in state 'deleted' so its
// dead letters — the record of what a consumer was never told — outlive it.
func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if _, ok := scoped(w, r); !ok {
		return
	}
	ns, id := r.PathValue("namespace"), r.PathValue("subscription")

	// Check ownership before proposing. The command carries only an id, so
	// without this a key scoped to one namespace could delete a subscription
	// belonging to another — the scope check in scoped() covers the path, and
	// the path does not otherwise constrain which subscription is named.
	sub, err := s.Store.Subscription(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sub.Namespace != ns) {
		notFound(w, "subscription")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if err := s.webhooker().Webhook(r.Context(), store.WebhookCommand{
		Op: store.WebhookDelete, ID: id, At: time.Now().UTC(),
	}); err != nil {
		s.writeFailure(w, r, err)
		return
	}
	ok(w, "Subscription deleted successfully", map[string]any{"id": id})
}

func newSubscriptionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate subscription id: %w", err)
	}
	return "sub_" + hex.EncodeToString(b[:]), nil
}

// checkWebhookTarget validates a delivery target and returns it normalised.
//
// It is called twice in the life of a subscription: here, when it is
// registered, and again at delivery time. Both are necessary and neither is
// sufficient. Checking only at creation means a name that resolves publicly
// today can resolve to 169.254.169.254 tomorrow — the operator of the target
// domain decides that, not this node. Checking only at delivery means a bad
// target is accepted, stored, and fails silently later, which the operator
// discovers by noticing an outage nobody reported.
func (s *Server) checkWebhookTarget(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("target_url is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("target_url must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("target_url has no host")
	}
	if s.AllowPrivateWebhookTargets {
		return u.String(), nil
	}
	host := u.Hostname()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("target_url host %q does not resolve: %w", host, err)
	}
	for _, a := range addrs {
		if !publicIP(a.IP) {
			return "", fmt.Errorf("target_url host %q resolves to %s, which is not a public address. "+
				"The node would be making this request from inside your own network; "+
				"set DEDI_WEBHOOK_ALLOW_PRIVATE=1 if that is what you intend", host, a.IP)
		}
	}
	return u.String(), nil
}

// publicIP reports whether an address is one this node should be willing to
// send an operator-supplied request to.
//
// The unroutable ranges are refused for the obvious reason. 169.254.0.0/16 is
// called out separately because it is the one that actually matters here: on
// every major cloud platform 169.254.169.254 serves instance credentials to
// whatever asks, and "make the server fetch a URL for me" is precisely the
// capability a subscription grants.
func publicIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback(), ip.IsUnspecified(), ip.IsMulticast(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsPrivate():
		return false
	}
	return true
}
