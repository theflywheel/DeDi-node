package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

// The publisher plane (design.md §5.3). Every route here is authenticated by an
// Ed25519 signature over the request and scoped to the key's namespace, and
// every one of them appends — nothing is ever edited or deleted, so the log
// stays the audit trail governance.md requires.
//
// design.md spells these as `.../records/{id}:publish`. Go's ServeMux matches
// wildcards on whole path segments, so `{record}:publish` cannot be expressed;
// the verb is its own segment instead. The shape is otherwise as specified.

type publishRequest struct {
	Payload json.RawMessage `json:"payload"`
}

type revokeRequest struct {
	Reason string `json:"reason"`
}

// writeEnabled reports whether the node has a write plane at all. With no
// publisher keys configured the routes are not registered, so an operator who
// has not opened the write plane cannot be probed for one.
func (s *Server) writeEnabled() bool {
	return s.Auth != nil && s.Auth.Keys != nil && s.Auth.Keys.Len() > 0
}

// denyWrite renders an authentication failure in the node's error envelope.
func denyWrite(w http.ResponseWriter, _ *http.Request, err error) {
	status := publisher.StatusFor(err)
	code := "UNAUTHORIZED"
	switch status {
	case http.StatusForbidden:
		code = "FORBIDDEN"
	case http.StatusBadRequest:
		code = "INVALID_REQUEST"
	}
	writeErr(w, status, code, err.Error())
}

// scoped resolves the verified publisher key and checks it may write to the
// namespace in the path. A key valid for one namespace answering for another is
// the escalation governance.md's per-namespace grants exist to prevent.
func scoped(w http.ResponseWriter, r *http.Request) (publisher.Key, bool) {
	key, ok := publisher.KeyFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "request is not signed")
		return publisher.Key{}, false
	}
	if err := key.Authorizes(r.PathValue("namespace")); err != nil {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return publisher.Key{}, false
	}
	return key, true
}

// decodePayload reads {"payload": {...}} and rejects anything that is not a
// JSON object — a bare string or array would be valid JSON but meaningless as
// a record payload, and the read plane assumes an object.
func decodePayload(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "body must be JSON of the form {\"payload\": {...}}")
		return nil, false
	}
	if len(req.Payload) == 0 {
		badRequest(w, "payload is required")
		return nil, false
	}
	var obj map[string]any
	if err := json.Unmarshal(req.Payload, &obj); err != nil {
		badRequest(w, "payload must be a JSON object")
		return nil, false
	}
	return req.Payload, true
}

// appendAs performs the write and renders the new version. CreatedBy carries
// the publisher key id, so the log records which credential made each change.
func (s *Server) appendAs(w http.ResponseWriter, r *http.Request, key publisher.Key, in store.AppendInput, msg string) {
	in.CreatedBy = "publisher:" + key.KID
	e, err := s.Store.Append(r.Context(), in)
	if err != nil {
		// A malformed write is the caller's fault, not the node's; only
		// genuine failures should read as 500.
		if errors.Is(err, store.ErrInvalidWrite) {
			badRequest(w, err.Error())
			return
		}
		internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Message: msg, Data: map[string]any{
		"namespace":   e.Namespace,
		"registry":    e.Registry,
		"record_name": e.RecordName,
		"version":     fmt.Sprintf("%d", e.Seq),
		"version_num": e.VersionNum,
		"state":       e.State,
		"digest":      fmt.Sprintf("%x", e.Digest),
		"created_by":  e.CreatedBy,
		"created_at":  e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
	}})
}

func (s *Server) putNamespace(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	payload, ok := decodePayload(w, r)
	if !ok {
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "namespace", Namespace: r.PathValue("namespace"), PayloadRaw: payload,
	}, "Namespace published successfully")
}

func (s *Server) putRegistry(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	payload, ok := decodePayload(w, r)
	if !ok {
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "registry", Namespace: r.PathValue("namespace"),
		Registry: r.PathValue("registry_name"), PayloadRaw: payload,
	}, "Registry published successfully")
}

func (s *Server) publishRecord(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	payload, ok := decodePayload(w, r)
	if !ok {
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "record", Namespace: r.PathValue("namespace"),
		Registry: r.PathValue("registry_name"), RecordName: r.PathValue("record_name"),
		PayloadRaw: payload, State: "live",
	}, "Record published successfully")
}

// revokeRecord appends a revoked version carrying the previous payload, so the
// record's content stays inspectable and only its state changes. Revocation is
// never a deletion: history has to stay provable.
func (s *Server) revokeRecord(w http.ResponseWriter, r *http.Request) {
	key, ok := scoped(w, r)
	if !ok {
		return
	}
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")

	current, err := s.Store.Resolve(r.Context(), "record", ns, reg, rec, nil, nil)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "record")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}

	var req revokeRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			badRequest(w, "body must be JSON of the form {\"reason\": \"...\"}")
			return
		}
	}
	payload := current.PayloadRaw
	if req.Reason != "" {
		var obj map[string]any
		if err := json.Unmarshal(current.PayloadRaw, &obj); err != nil {
			internal(w, err)
			return
		}
		obj["revocation_reason"] = req.Reason
		if payload, err = json.Marshal(obj); err != nil {
			internal(w, err)
			return
		}
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "record", Namespace: ns, Registry: reg, RecordName: rec,
		PayloadRaw: payload, State: "revoked",
	}, "Record revoked successfully")
}
