package api

import (
	"net/http"
	"time"
)

// stats reports how many requests this node has served. The answer is the
// persisted total plus whatever has accumulated since the last flush, so the
// number moves in real time rather than stepping once per flush interval.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	persisted, err := s.Store.RequestCounts(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	byClass := make(map[string]int64, len(counterClasses))
	var total int64
	for _, class := range counterClasses {
		n := persisted[class] + s.reqs.bucket(class).Load()
		byClass[class] = n
		total += n
	}
	data := map[string]any{
		"requests_served": total,
		"by_status_class": byClass,
	}
	if !s.startedAt.IsZero() {
		data["uptime_seconds"] = int64(time.Since(s.startedAt).Seconds())
	}
	ok(w, "Stats retrieved successfully", data)
}
