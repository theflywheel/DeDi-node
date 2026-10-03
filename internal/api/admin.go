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
	"github.com/theflywheel/DeDi-node/internal/refschemas"
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
	if obj == nil {
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

func versionTag(e store.Entry) string {
	return hex.EncodeToString(e.Digest) + "-" + e.State
}

func parseVersionTag(tag string) ([]byte, string, error) {
	digestHex, state, ok := strings.Cut(tag, "-")
	if !ok || state == "" {
		return nil, "", fmt.Errorf("missing state")
	}
	digest, err := hex.DecodeString(digestHex)
	if err != nil || len(digest) != sha256.Size {
		return nil, "", fmt.Errorf("bad digest")
	}
	return digest, state, nil
}

// preconditionOf reads the mandatory conditional-request header and turns it
// into a store precondition.
//
// Every write must state what it expects to be replacing:
// `If-Match: <digest>-<state>` to replace a known version, or
// `If-None-Match: *` to create one that must not exist yet. Both are covered by
// the signature (publisher.Preimage) and both are enforced inside the append
// transaction (store.checkPrecondition).
//
// Making this mandatory rather than optional is what closes replay. A captured
// signed write cannot be stripped of its precondition, so replaying it can only
// ever land on the exact payload and state it was written against — by which
// time that version has moved on, and the replay is a 412 instead of a silent
// revert to an older payload or state. The same rule gives lost-update
// protection for free: two operators editing one participant cannot overwrite
// each other unseen.
//
// Returns false when it has already answered the request.
func preconditionOf(w http.ResponseWriter, r *http.Request) (store.AppendInput, bool) {
	var in store.AppendInput
	ifMatch := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	ifNone := strings.TrimSpace(r.Header.Get("If-None-Match"))

	switch {
	case ifMatch != "" && ifNone != "":
		badRequest(w, "give either If-Match or If-None-Match, not both")
		return in, false
	case ifNone != "":
		if ifNone != "*" {
			badRequest(w, "If-None-Match must be * on a write")
			return in, false
		}
		in.ExpectedAbsent = true
	case ifMatch != "":
		// `*` would mean "any current version", which is precisely the
		// unconditional write this header exists to prevent here.
		digest, state, err := parseVersionTag(ifMatch)
		if err != nil {
			badRequest(w, "If-Match must be <hex digest>-<state> for the version being replaced, or use If-None-Match: * to create")
			return in, false
		}
		in.ExpectedPrevDigest = digest
		in.ExpectedPrevState = state
	default:
		writeErr(w, http.StatusPreconditionRequired, "PRECONDITION_REQUIRED",
			"writes must carry If-Match: <digest>-<state> or If-None-Match: *")
		return in, false
	}
	return in, true
}

// conflict renders a failed precondition. It carries the current digest so a
// caller that lost a race can re-read, merge and retry without a second lookup.
func conflict(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusPreconditionFailed, "PRECONDITION_FAILED", err.Error())
}

// unchanged collapses an exact re-publish into a no-op, so a double-clicked
// Save does not fork a participant's history with a duplicate version. This is
// a courtesy, not a safety property — replay is closed by checking the signed
// precondition against the current version before returning unchanged.
//
// Returns true when it has already answered the request.
func (s *Server) unchanged(w http.ResponseWriter, r *http.Request, state string, payload []byte, in store.AppendInput) bool {
	// A follower reads its own replica, which may not have applied the leader's
	// latest version yet. Answering 200 "unchanged" from that is a stale claim
	// about the current version: a client republishing what this replica happens
	// to hold, with a matching If-Match, would be told it succeeded while the
	// leader still has something else. Let the append redirect instead — the
	// leader can make the same no-op decision correctly.
	if s.onFollower() {
		return false
	}
	current, err := s.Store.ResolveCurrentForWrite(r.Context(), in)
	if errors.Is(err, store.ErrVersionConflict) {
		conflict(w, err)
		return true
	}
	if err != nil {
		return false // absent, or a real error the append will surface
	}
	switch {
	case in.ExpectedAbsent:
		conflict(w, fmt.Errorf("%w: expected no existing version, found %s",
			store.ErrVersionConflict, versionTag(current)))
		return true
	case in.ExpectedPrevDigest != nil && !bytes.Equal(in.ExpectedPrevDigest, current.Digest):
		conflict(w, fmt.Errorf("%w: expected version %x-%s, found %s",
			store.ErrVersionConflict, in.ExpectedPrevDigest, in.ExpectedPrevState, versionTag(current)))
		return true
	case in.ExpectedPrevDigest != nil && in.ExpectedPrevState != "" && in.ExpectedPrevState != current.State:
		conflict(w, fmt.Errorf("%w: expected version %x-%s, found %s",
			store.ErrVersionConflict, in.ExpectedPrevDigest, in.ExpectedPrevState, versionTag(current)))
		return true
	}
	newDigest, err := PayloadDigest(payload)
	if err != nil {
		badRequest(w, "payload is not valid JSON")
		return true
	}
	if newDigest == hex.EncodeToString(current.Digest) && current.State == state {
		writeJSON(w, http.StatusOK, envelope{Message: "Record already at this version; no new version appended",
			Data: versionData(current, true)})
		return true
	}
	return false
}

// versionData renders a written (or unchanged) version.
func versionData(e store.Entry, unchanged bool) map[string]any {
	return map[string]any{
		"namespace":   e.Namespace,
		"registry":    e.Registry,
		"record_name": e.RecordName,
		// version_id is the name the read plane takes this value under
		// (?version_id=); version is kept because existing callers read it
		// (#68). version_num is a different quantity: the ordinal within this
		// resource, not the log sequence a pin takes.
		"version_id":  versionID(e.Seq),
		"version":     versionID(e.Seq),
		"version_num": e.VersionNum,
		"state":       e.State,
		"digest":      hex.EncodeToString(e.Digest),
		"version_tag": versionTag(e),
		"created_by":  e.CreatedBy,
		"created_at":  e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
		"unchanged":   unchanged,
	}
}

// appendAs performs the write and renders the new version. CreatedBy carries
// the publisher key id, so the log records which credential made each change.
func (s *Server) appendAs(w http.ResponseWriter, r *http.Request, key publisher.Key, in store.AppendInput, msg string) {
	in.CreatedBy = "publisher:" + key.KID
	e, err := s.writer().Append(r.Context(), in)
	if err != nil {
		s.writeFailure(w, r, err)
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
	in, ok := preconditionOf(w, r)
	if !ok {
		return
	}
	in.EntryType, in.Namespace, in.PayloadRaw = "namespace", r.PathValue("namespace"), payload
	s.appendAs(w, r, key, in, "Namespace published successfully")
}

// builtinSchemaPrefix marks a registry payload's `schema` field as a
// reference to one of the five reference registry schemas the DeDi standard
// ships (internal/refschemas), rather than a hand-pasted schema object. e.g.
// `"schema": "builtin:public_key"`.
//
// Operators pasting schemas by hand is how two nodes both claiming a
// "public_key" registry end up enforcing different shapes for it. Resolving
// the reference here, before the payload is stored, means the registry
// record on disk always carries the full schema — readers and the schema
// validator (store.ValidateAgainstSchema) never need to know built-in refs
// exist at all.
const builtinSchemaPrefix = "builtin:"

// resolveBuiltinSchema rewrites a registry payload's `schema` field in place
// when it names a built-in ("builtin:<name>"), replacing it with the
// resolved schema object. A payload whose `schema` is absent, already an
// object, or not a recognized built-in name is returned unchanged (the
// latter case is left for the normal schema/validation path to reject).
func resolveBuiltinSchema(payload json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return payload, nil
	}
	rawSchema, ok := obj["schema"]
	if !ok {
		return payload, nil
	}
	var ref string
	if err := json.Unmarshal(rawSchema, &ref); err != nil {
		return payload, nil // not a string, so not a built-in reference
	}
	name, isBuiltin := strings.CutPrefix(ref, builtinSchemaPrefix)
	if !isBuiltin {
		return payload, nil
	}
	resolved, found := refschemas.Lookup(name)
	if !found {
		return nil, fmt.Errorf("unknown built-in schema %q; available: %s", name, strings.Join(refschemas.Names(), ", "))
	}
	obj["schema"] = resolved
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return out, nil
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
	payload, err := resolveBuiltinSchema(payload)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	in, ok := preconditionOf(w, r)
	if !ok {
		return
	}
	in.EntryType, in.Namespace = "registry", r.PathValue("namespace")
	in.Registry, in.PayloadRaw = r.PathValue("registry_name"), payload
	s.appendAs(w, r, key, in, "Registry published successfully")
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
	in, ok := preconditionOf(w, r)
	if !ok {
		return
	}
	in.EntryType, in.Namespace, in.Registry, in.RecordName = "record", ns, reg, rec
	in.PayloadRaw, in.State = payload, "live"
	if s.unchanged(w, r, "live", payload, in) {
		return
	}
	s.appendAs(w, r, key, in, "Record published successfully")
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
	in, okPre := preconditionOf(w, r)
	if !okPre {
		return
	}
	if in.ExpectedAbsent {
		badRequest(w, "revoke replaces an existing record; use If-Match, not If-None-Match")
		return
	}
	in.EntryType, in.Namespace, in.Registry, in.RecordName = "record", ns, reg, rec
	// Revoking an already-revoked record is a no-op, not a second revocation —
	// but only the leader may say so. On a follower `current` came from a replica
	// that may not have applied the leader's latest version, so "already
	// revoked" is a guess; fall through and let the append redirect.
	if current.State == "revoked" && !s.onFollower() {
		lockedCurrent, err := s.Store.ResolveCurrentForWrite(r.Context(), in)
		if errors.Is(err, store.ErrVersionConflict) {
			conflict(w, err)
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		if lockedCurrent.State != "revoked" {
			current = lockedCurrent
		} else {
			writeJSON(w, http.StatusOK, envelope{Message: "Record is already revoked; no new version appended",
				Data: versionData(lockedCurrent, true)})
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
	in.PayloadRaw, in.State = payload, "revoked"
	s.appendAs(w, r, key, in, "Record revoked successfully")
}
