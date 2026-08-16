package api

import (
	"net/http"
	"time"

	"github.com/theflywheel/DeDi-node/internal/checkpoint"
	"github.com/theflywheel/DeDi-node/internal/cluster"
	"github.com/theflywheel/DeDi-node/internal/dedifile"
	"github.com/theflywheel/DeDi-node/internal/delegation"
	"github.com/theflywheel/DeDi-node/internal/domainproof"
	"github.com/theflywheel/DeDi-node/internal/network"
	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

type Server struct {
	Store       *store.Store
	CP          *checkpoint.Checkpointer
	TTL         int    // cache hint surfaced in ttl fields (seconds)
	VerifierKey string // node verifier key, injected into the explorer page (may be empty)
	DemoURL     string // target of the pages' "Demo" nav tab; empty falls back to defaultDemoURL

	// NodeName labels this node in the network view. Empty falls back to the
	// log origin, which is always set and always distinct between nodes.
	NodeName string

	// Network observes the other nodes carrying this directory network. nil on
	// a standalone node, which then reports a network of one — accurately.
	Network *network.Monitor

	// WitnessTarget and WitnessTargetURL name the node this one witnesses, so
	// the network view can distinguish a peer it merely reaches from the peer
	// whose history it is actually proving. Empty when witnessing nothing.
	WitnessTarget    string
	WitnessTargetURL string

	// WitnessTargetKey is the target's public verifier key. It is published so a
	// visitor's browser can check the target's checkpoint signature itself
	// rather than believing this node's report of it. Public by construction —
	// a verifier key is what you hand out precisely so others can check you.
	WitnessTargetKey string

	// WitnessHealth reports whether the witness loop is still running. Supplied
	// as a function rather than a *witness.Witness so this package does not
	// import witness — witness's own tests import this one, and the cycle would
	// not build.
	WitnessHealth func() WitnessState

	// Writer appends to the log. nil writes straight to Store, which is the
	// standalone case; in a cluster it is the Raft proposer, and writes that
	// arrive at a follower are redirected to the leader rather than applied
	// locally.
	Writer Appender

	// Cluster reports Raft membership and leadership, for the network view and
	// for redirecting writes. nil on an unreplicated node, which then reports a
	// cluster of one — accurately.
	Cluster func() cluster.State

	// WildcardNamespaces limits which namespaces may answer a Beckn wildcard
	// lookup (design.md:256). nil means no restriction — permitted only while
	// the write plane is closed; see serve().
	WildcardNamespaces []string

	// AdminAuth gates the admin surface at the deployment level: the operator
	// of this node, as opposed to the publishers whose keys sign the writes.
	// nil leaves the surface reachable to anyone, with the signature still the
	// only thing that can actually change the log.
	AdminAuth *AdminAuth

	// PublicURL is this node's externally reachable base URL. Behind a proxy
	// the request's own Host is the proxy's, so a child told to enrol against
	// it cannot reach us; this is what the child is handed instead.
	PublicURL string

	// OnDelegation is called when a child completes enrolment, so the daemon
	// can start witnessing it without a restart. nil disables that.
	OnDelegation func(delegation.Record)

	// ChildWitnessHealth reports the liveness of the witness loop watching one
	// child, by child origin. Separate from WitnessHealth, which covers the
	// single ring peer this node witnesses: there is one of those and one loop
	// per child, and collapsing them would make a stalled child-witness
	// invisible behind a healthy ring one.
	//
	// Same shape as WitnessHealth and for the same reason — a function, so this
	// package does not import witness and create a cycle.
	ChildWitnessHealth func(childOrigin string) (WitnessState, bool)

	// Auth verifies signed writes. nil, or holding no keys, leaves the write
	// plane closed and its routes unregistered.
	Auth *publisher.Authenticator

	// DeliveryHealth reports whether the webhook push loop is running, and
	// DeliveryRetries how many consecutive failures a given subscription is
	// showing. Functions rather than a *webhook.Deliverer for the reason
	// WitnessHealth is one: this package must not import the subsystem.
	//
	// Both nil on a node with no delivery loop, which the console renders as
	// "not running" rather than as healthy — the safer default, because a
	// subscription with an empty queue looks identical either way.
	DeliveryHealth  func() DeliveryState
	DeliveryRetries func(subscriptionID string) (int, string)

	// AllowPrivateWebhookTargets lets a subscription point at an address that is
	// not publicly routable. Off by default: the node fetches these URLs itself,
	// from inside the operator's network, so an unchecked target is a request
	// forgery primitive — and on every major cloud platform the link-local
	// metadata address hands out instance credentials to whatever asks.
	//
	// Legitimately needed when the consumer is a sibling service on a private
	// network, which is the common case in a single-VPC deployment, so it is a
	// switch rather than a prohibition.
	AllowPrivateWebhookTargets bool

	// DNSResolver resolves the TXT challenge that binds a namespace to its
	// declared domain (internal/domainproof, task #56). nil uses the system
	// resolver; tests supply their own zone rather than depending on public DNS.
	DNSResolver domainproof.Resolver

	reqs      counters  // requests served since the last flush (see counter.go)
	startedAt time.Time // set by Handler

	// dedifileCache memoizes the signed file-publication build across requests
	// that would produce identical bytes (task #60). Zero value is a usable,
	// empty cache, so a Server built by hand in a test gets it too.
	dedifileCache dedifile.Cache
}

func (s *Server) Handler() http.Handler {
	s.startedAt = time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dedi/lookup/{namespace}", s.lookupNamespace)
	mux.HandleFunc("GET /dedi/lookup/{namespace}/{registry_name}", s.lookupRegistry)
	mux.HandleFunc("GET /dedi/lookup/{namespace}/{registry_name}/{record_name}", s.lookupRecord)
	mux.HandleFunc("GET /dedi/query/{namespace}", s.queryNamespace)
	mux.HandleFunc("GET /dedi/query/{namespace}/{registry_name}", s.queryRegistry)
	mux.HandleFunc("GET /dedi/versions/{namespace}", s.versionsNamespace)
	mux.HandleFunc("GET /dedi/versions/{namespace}/{registry_name}", s.versionsRegistry)
	mux.HandleFunc("GET /dedi/versions/{namespace}/{registry_name}/{record_name}", s.versionsRecord)
	mux.HandleFunc("GET /dedi/log/checkpoint", s.logCheckpoint)
	mux.HandleFunc("GET /dedi/log/proof/consistency", s.logConsistency)
	mux.HandleFunc("GET /dedi/stats", s.stats)
	mux.HandleFunc("GET /dedi/network", s.networkView)
	mux.HandleFunc("GET /dedi/delegations/{namespace}", s.listDelegations)
	// File-publication model (docs/spec/lfdt/docs/publishing-dedi-files.md):
	// this node as a publisher, alongside the API surface above. The
	// well-known path is normative (RFC 8615); /dedi-files/ is where its
	// manifest points, chosen instead of the spec's RECOMMENDED /dedi/
	// directory because /dedi/ is already this node's API prefix.
	mux.HandleFunc("GET /.well-known/dedi.index.json", s.wellKnownIndex)
	mux.HandleFunc("GET /dedi-files/{namespace}/{file}", s.dedifileByNamespace)
	// Enrolment is authenticated by its one-time token, not by a publisher
	// signature — a child has no key yet, which is what it is asking for.
	//
	// It is registered off the /dedi/ prefix: that prefix belongs to the DeDi
	// standard (docs/spec/lfdt/api/openapi.yaml), which reserves it for
	// lookup/query/versions, not for this node's own delegation mechanism.
	mux.HandleFunc("POST /enrol", s.enrolChild)
	// Deprecated alias for the old, spec-prefix-squatting path. Kept only
	// until every deployed ring node has upgraded to call POST /enrol
	// instead; remove once that rollout is complete.
	mux.HandleFunc("POST /dedi/enrol", s.deprecatedEnrolChild)
	mux.HandleFunc("GET /healthz", s.healthz)
	// Operational, not part of the read plane: replica lag and peer
	// reachability in the format a scraper already speaks, so a replica falling
	// behind pages someone instead of waiting to be noticed on a dashboard
	// (task #30).
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /{$}", s.explorer)
	mux.HandleFunc("GET /verify", s.verify)
	mux.HandleFunc("GET /verify/{$}", s.verify)
	mux.HandleFunc("GET /static/verify.js", s.verifyScript)
	mux.HandleFunc("GET /docs", s.docs)
	mux.HandleFunc("GET /docs/{$}", s.docs)
	// Every markdown document in docs/, rendered from the copy embedded in this
	// binary. Served by the node itself so a deployment can explain itself
	// without reaching the internet — see the package comment on embed.go.
	mux.HandleFunc("GET /docs/{page}", s.docPage)
	// The console is only served where there is something for it to drive. On a
	// read-only node it is a form soliciting a private key for a write plane
	// that does not exist — attack surface with no counterpart.
	if s.writeEnabled() {
		mux.Handle("GET /admin", s.AdminAuth.gate(http.HandlerFunc(s.admin)))
		mux.Handle("GET /admin/{$}", s.AdminAuth.gate(http.HandlerFunc(s.admin)))
	}

	// Publisher plane. Registered only when the node holds publisher keys, so a
	// read-only node has no write surface to probe at all (governance.md:
	// "enforcement today is structural").
	if s.writeEnabled() {
		// Two gates, and they answer different questions. AdminAuth asks
		// whether you may reach this node's admin surface at all; the
		// signature asks who is writing and whether that key may write here,
		// and it is what puts publisher:<kid> on the resulting version. A
		// shared password cannot attribute a write, so it never replaces the
		// signature — it only fronts it.
		write := func(pattern string, h http.HandlerFunc) {
			mux.Handle(pattern, s.AdminAuth.gate(s.Auth.Require(h, denyWrite)))
		}
		write("PUT /admin/namespaces/{namespace}", s.putNamespace)
		write("POST /admin/namespaces/{namespace}/children", s.createChild)
		write("POST /admin/namespaces/{namespace}/children/{child}/revoke", s.revokeChild)
		// Namespace-to-domain binding (task #56, docs/spec-gaps.md G8). On the
		// write plane rather than the read plane on purpose: the verdict is
		// this node's own bookkeeping, and the standard's lookup response
		// schema declares no field to carry it.
		write("GET /admin/namespaces/{namespace}/domain", s.showDomain)
		write("POST /admin/namespaces/{namespace}/domain/verify", s.verifyDomain)
		write("DELETE /admin/namespaces/{namespace}/domain", s.unverifyDomain)
		write("PUT /admin/namespaces/{namespace}/registries/{registry_name}", s.putRegistry)
		write("POST /admin/namespaces/{namespace}/registries/{registry_name}/records/{record_name}/publish", s.publishRecord)
		write("POST /admin/namespaces/{namespace}/registries/{registry_name}/records/{record_name}/revoke", s.revokeRecord)
		write("POST /admin/namespaces/{namespace}/registries/{registry_name}/subscriptions", s.createSubscription)
		write("GET /admin/namespaces/{namespace}/subscriptions", s.listSubscriptions)
		write("DELETE /admin/namespaces/{namespace}/subscriptions/{subscription}", s.deleteSubscription)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		notFound(w, "route")
	})
	// CORS sits outside the counter so the header is present on every response
	// the counter sees, including the ones it does not count.
	return readPlaneCORS(s.counted(mux))
}
