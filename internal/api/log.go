package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func (s *Server) logCheckpoint(w http.ResponseWriter, r *http.Request) {
	_, text, err := s.Store.LatestCheckpoint(r.Context())
	if errors.Is(err, store.ErrNoCheckpoint) {
		_, text, err = s.CP.PublishNow(r.Context())
	}
	if err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(text))
}

func (s *Server) logConsistency(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	oldSize, err1 := strconv.ParseInt(q.Get("old"), 10, 64)
	newSize, err2 := strconv.ParseInt(q.Get("new"), 10, 64)
	if err1 != nil || err2 != nil || oldSize < 1 || newSize < oldSize {
		badRequest(w, "old and new must be integers with 1 <= old <= new")
		return
	}
	size, err := s.Store.TreeSize(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	if newSize > size {
		badRequest(w, "new exceeds current tree size")
		return
	}
	proof, err := s.Store.ProveConsistency(r.Context(), oldSize, newSize)
	if err != nil {
		internal(w, err)
		return
	}
	path := make([]string, len(proof))
	for i, h := range proof {
		path[i] = base64.StdEncoding.EncodeToString(h[:])
	}
	ok(w, "Consistency proof retrieved successfully", map[string]any{
		"old_size": oldSize, "new_size": newSize, "proof": path,
	})
}
