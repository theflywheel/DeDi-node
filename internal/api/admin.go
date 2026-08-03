package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/theflywheel/DeDi-node/internal/publisher"
	"github.com/theflywheel/DeDi-node/internal/store"
)

func (s *Server) mountAdmin(mux *http.ServeMux) {
	if s.PublisherKeys == nil || s.PublisherKeys.Len() == 0 {
		return
	}
	auth := (&publisher.Authenticator{Keys: s.PublisherKeys}).Require
	mux.Handle("PUT /admin/namespaces/{namespace}", auth(http.HandlerFunc(s.putNamespace), denyPublisher))
	mux.Handle("PUT /admin/namespaces/{namespace}/registries/{registry_name}", auth(http.HandlerFunc(s.putRegistry), denyPublisher))
	mux.Handle("POST /admin/namespaces/{namespace}/registries/{registry_name}/records/{publish_target}", auth(http.HandlerFunc(s.publishRecord), denyPublisher))
}

func denyPublisher(w http.ResponseWriter, r *http.Request, err error) {
	status := publisher.StatusFor(err)
	code := "INVALID_REQUEST"
	if status == http.StatusUnauthorized {
		code = "UNAUTHORIZED"
	} else if status == http.StatusForbidden {
		code = "FORBIDDEN"
	}
	writeErr(w, status, code, err.Error())
}

func publisherKey(w http.ResponseWriter, r *http.Request, namespace string) (publisher.Key, bool) {
	key, ok := publisher.KeyFrom(r.Context())
	if !ok {
		internal(w, errors.New("publisher key missing from authenticated request context"))
		return publisher.Key{}, false
	}
	if err := key.Authorizes(namespace); err != nil {
		denyPublisher(w, r, err)
		return publisher.Key{}, false
	}
	return key, true
}

func readPayload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		badRequest(w, err.Error())
		return nil, false
	}
	if len(body) == 0 {
		badRequest(w, "payload JSON body is required")
		return nil, false
	}
	return body, true
}

func appendErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "parent")
		return
	}
	if errors.Is(err, store.ErrVersionConflict) {
		writeErr(w, http.StatusConflict, "VERSION_CONFLICT", err.Error())
		return
	}
	badRequest(w, err.Error())
}

func expectedPrevVersion(w http.ResponseWriter, r *http.Request) (*int32, bool) {
	raw := r.URL.Query().Get("expected_version")
	if raw == "" {
		badRequest(w, "expected_version query parameter is required")
		return nil, false
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		badRequest(w, "expected_version must be a non-negative integer")
		return nil, false
	}
	v := int32(n)
	return &v, true
}

func (s *Server) putNamespace(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	key, okAuth := publisherKey(w, r, ns)
	if !okAuth {
		return
	}
	body, okBody := readPayload(w, r)
	if !okBody {
		return
	}
	expected, okExpected := expectedPrevVersion(w, r)
	if !okExpected {
		return
	}
	e, err := s.Store.Append(r.Context(), store.AppendInput{
		EntryType:           "namespace",
		Namespace:           ns,
		PayloadRaw:          body,
		State:               r.URL.Query().Get("state"),
		CreatedBy:           key.KID,
		ExpectedPrevVersion: expected,
	})
	if err != nil {
		appendErr(w, err)
		return
	}
	versions, err := s.Store.Versions(r.Context(), "namespace", ns, "", "")
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, "Namespace published successfully", namespaceData(e, versions, s.TTL))
}

func (s *Server) putRegistry(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	key, okAuth := publisherKey(w, r, ns)
	if !okAuth {
		return
	}
	body, okBody := readPayload(w, r)
	if !okBody {
		return
	}
	expected, okExpected := expectedPrevVersion(w, r)
	if !okExpected {
		return
	}
	e, err := s.Store.Append(r.Context(), store.AppendInput{
		EntryType:           "registry",
		Namespace:           ns,
		Registry:            reg,
		PayloadRaw:          body,
		State:               r.URL.Query().Get("state"),
		CreatedBy:           key.KID,
		ExpectedPrevVersion: expected,
	})
	if err != nil {
		appendErr(w, err)
		return
	}
	versions, err := s.Store.Versions(r.Context(), "registry", ns, reg, "")
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, "Registry published successfully", registryData(e, versions, s.TTL))
}

func (s *Server) publishRecord(w http.ResponseWriter, r *http.Request) {
	ns, reg := r.PathValue("namespace"), r.PathValue("registry_name")
	target := r.PathValue("publish_target")
	rec, okPublish := strings.CutSuffix(target, ":publish")
	if !okPublish || rec == "" {
		notFound(w, "route")
		return
	}
	key, okAuth := publisherKey(w, r, ns)
	if !okAuth {
		return
	}
	body, okBody := readPayload(w, r)
	if !okBody {
		return
	}
	expected, okExpected := expectedPrevVersion(w, r)
	if !okExpected {
		return
	}
	e, err := s.Store.Append(r.Context(), store.AppendInput{
		EntryType:           "record",
		Namespace:           ns,
		Registry:            reg,
		RecordName:          rec,
		PayloadRaw:          body,
		State:               r.URL.Query().Get("state"),
		CreatedBy:           key.KID,
		ExpectedPrevVersion: expected,
	})
	if err != nil {
		appendErr(w, err)
		return
	}
	versions, err := s.Store.Versions(r.Context(), "record", ns, reg, rec)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, "Record published successfully", recordData(e, versions, s.TTL))
}
