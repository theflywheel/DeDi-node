package api

import (
	"errors"
	"net/http"

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
