package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// Discovery: GET /dedi/query/{namespace}/{registry_name}?domain=retail
//
// A filter on the existing query path rather than a new endpoint, and that is
// design.md's decision rather than this file's: §120 says attribute filtering
// over payload fields — role, domain, city/coverage, status — is "a namespaced
// extension backed by the JSONB GIN index", required for gateway discovery.
// A separate endpoint would have invented a second shape for a question the
// spec already places here.
//
// It is additive: /dedi/query without the parameter behaves exactly as it did,
// still never reaching into the payload. Only the presence of ?domain= opts a
// caller into the extension, so a spec-conformant client cannot be surprised
// by it.

// discoveredDTO is what a router needs and nothing else: who this is, where to
// reach them, and what to check them with. It is deliberately not the full
// record — a caller acting on this should read the record and verify it, and
// listing everything here would invite them not to.
type discoveredDTO struct {
	SubscriberID string `json:"subscriber_id"`
	RecordID     string `json:"record_id"`
	RecordName   string `json:"record_name"`
	NamespaceID  string `json:"namespace_id"`
	URL          string `json:"url"`
	Type         string `json:"type,omitempty"`
	Digest       string `json:"digest"`
	State        string `json:"state"`
	UpdatedAt    string `json:"updated_at"`
	// LookupURL is where to go and verify. The list is a starting point, not an
	// answer to be trusted: this node assembled it, and only the record and its
	// inclusion proof say what was actually published.
	LookupURL string `json:"lookup_url"`
}

// discoveryFields are the payload fields the response projects. Read with a
// tolerant decode so a record missing any of them still appears — a participant
// that omitted `type` is still somewhere a router can send business.
type discoveryFields struct {
	SubscriberID string `json:"subscriber_id"`
	URL          string `json:"url"`
	Type         string `json:"type"`
}

// queryByDomain answers "who serves domain X" within one registry.
func (s *Server) queryByDomain(w http.ResponseWriter, r *http.Request, ns, reg, domain string) {
	// Confined to the eligible namespaces for the reason the store documents at
	// more length: a discovery caller has no subscriber_id it already believed
	// in, so without the allowlist anyone able to publish on this node could
	// insert themselves as a destination for any domain.
	entries, err := s.Store.ServingDomain(r.Context(), domain, s.WildcardNamespaces, 0)
	if errors.Is(err, store.ErrInvalidFilter) {
		badRequest(w, err.Error())
		return
	}
	if err != nil {
		internal(w, err)
		return
	}

	out := make([]discoveredDTO, 0, len(entries))
	for _, e := range entries {
		// The registry in the path scopes the answer. ServingDomain searches
		// every eligible namespace, which is what discovery means, but a caller
		// asked about this registry and should not be handed another one's
		// participants.
		if e.Registry != reg {
			continue
		}
		var f discoveryFields
		json.Unmarshal(e.PayloadRaw, &f)
		out = append(out, discoveredDTO{
			SubscriberID: f.SubscriberID, URL: f.URL, Type: f.Type,
			RecordID:   e.Namespace + "/" + e.Registry + "/" + e.RecordName,
			RecordName: e.RecordName, NamespaceID: e.Namespace,
			Digest: hex.EncodeToString(e.Digest), State: e.State,
			UpdatedAt: fmtTime(e.CreatedAt),
			LookupURL: "/dedi/lookup/" + e.Namespace + "/" + e.Registry + "/" + e.RecordName,
		})
	}

	// Short-lived, and for the same reason a latest-version lookup is: this
	// answer goes stale exactly when a revocation lands. A long max-age here
	// would reintroduce the staleness window that push exists to close — on the
	// surface that decides where traffic is *sent*, which is worse than the one
	// that decides whether a signature is accepted.
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(s.TTL))

	ok(w, "Participants retrieved successfully", map[string]any{
		"namespace_id":  ns,
		"registry_id":   ns + "/" + reg,
		"registry_name": reg,
		"domain":        domain,
		"total":         len(out),
		"participants":  out,
		"ttl":           s.TTL,
	})
}
