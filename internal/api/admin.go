package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// PayloadDigest is the digest the store records for a payload: SHA-256 over
// the compacted JSON. Recomputed here so a caller's precondition and a
// replayed write are compared against exactly what was stored.
func PayloadDigest(payload []byte) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// precondition enforces If-Match against the record's current digest, and
// collapses an exact replay into a no-op.
//
// Two different problems, one place. If-Match is lost-update protection: two
// operators editing the same participant should not silently overwrite each
// other. The replay check is what makes a double-clicked Save — or a captured
// request replayed inside the signature's timestamp window — stop forking a
// participant's history.
//
// Returns the existing entry and true when the caller should stop.
func (s *Server) precondition(w http.ResponseWriter, r *http.Request, ns, reg, rec, state string, payload []byte) (store.Entry, bool) {
	current, err := s.Store.Resolve(r.Context(), "record", ns, reg, rec, nil, nil)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Nothing published yet. An If-Match asking for a specific version
		// cannot be satisfied; If-Match: * means "must already exist".
		if m := r.Header.Get("If-Match"); m != "" {
			writeErr(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED",
				"If-Match was given but the record does not exist yet")
			return store.Entry{}, true
		}
		return store.Entry{}, false
	case err != nil:
		internal(w, err)
		return store.Entry{}, true
	}

	currentDigest := hex.EncodeToString(current.Digest)
	if m := strings.Trim(r.Header.Get("If-Match"), `"`); m != "" && m != "*" && m != currentDigest {
		writeErr(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED",
			"record has changed: current digest is "+currentDigest)
		return store.Entry{}, true
	}

	newDigest, err := PayloadDigest(payload)
	if err != nil {
		badRequest(w, "payload is not valid JSON")
		return store.Entry{}, true
	}
	if newDigest == currentDigest && current.State == state {
		// Byte-identical to what is already live: return the existing version
		// rather than appending a duplicate.
		writeJSON(w, http.StatusOK, envelope{Message: "Record already at this version; no new version appended",
			Data: versionData(current, true)})
		return current, true
	}
	return current, false
}

// versionData renders a written (or unchanged) version.
func versionData(e store.Entry, unchanged bool) map[string]any {
	return map[string]any{
		"namespace":   e.Namespace,
		"registry":    e.Registry,
		"record_name": e.RecordName,
		"version":     fmt.Sprintf("%d", e.Seq),
		"version_num": e.VersionNum,
		"state":       e.State,
		"digest":      hex.EncodeToString(e.Digest),
		"created_by":  e.CreatedBy,
		"created_at":  e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
		"unchanged":   unchanged,
	}
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
	writeJSON(w, http.StatusOK, envelope{Message: msg, Data: versionData(e, false)})
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
	ns, reg, rec := r.PathValue("namespace"), r.PathValue("registry_name"), r.PathValue("record_name")
	if _, stop := s.precondition(w, r, ns, reg, rec, "live", payload); stop {
		return
	}
	s.appendAs(w, r, key, store.AppendInput{
		EntryType: "record", Namespace: ns, Registry: reg, RecordName: rec,
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
	if m := strings.Trim(r.Header.Get("If-Match"), `"`); m != "" && m != "*" && m != hex.EncodeToString(current.Digest) {
		writeErr(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED",
			"record has changed: current digest is "+hex.EncodeToString(current.Digest))
		return
	}
	// Revoking an already-revoked record is a no-op, not a second revocation.
	if current.State == "revoked" {
		writeJSON(w, http.StatusOK, envelope{Message: "Record is already revoked; no new version appended",
			Data: versionData(current, true)})
		return
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
