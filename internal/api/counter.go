package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Request counters are bucketed by status class. Anything outside 2xx/4xx/5xx
// (1xx, 3xx) folds into "other" so the totals always reconcile.
const (
	class2xx   = "2xx"
	class4xx   = "4xx"
	class5xx   = "5xx"
	classOther = "other"
)

var counterClasses = []string{class2xx, class4xx, class5xx, classOther}

// counters holds the requests served since the last flush. The hot path is a
// single lock-free add; durability comes from the periodic flush, not from
// touching the database per request.
type counters struct {
	c2xx, c4xx, c5xx, cOther atomic.Int64
}

func (c *counters) bucket(class string) *atomic.Int64 {
	switch class {
	case class2xx:
		return &c.c2xx
	case class4xx:
		return &c.c4xx
	case class5xx:
		return &c.c5xx
	default:
		return &c.cOther
	}
}

func classOf(status int) string {
	switch {
	case status >= 200 && status < 300:
		return class2xx
	case status >= 400 && status < 500:
		return class4xx
	case status >= 500:
		return class5xx
	default:
		return classOther
	}
}

// statusRecorder captures the status code for the counter. A handler that never
// calls WriteHeader implicitly returns 200, which is the zero-value default.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// counted wraps h so every served request lands in a counter. The dashboard's
// own traffic is excluded — the page polls /dedi/stats on a timer, and counting
// that would make the number climb with nobody using the node.
func (s *Server) counted(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if selfTraffic(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		h.ServeHTTP(rec, r)
		s.reqs.bucket(classOf(rec.status)).Add(1)
	})
}

func selfTraffic(path string) bool {
	return path == "/" || path == "/docs" || path == "/docs/" ||
		path == "/dedi/stats" || strings.HasPrefix(path, "/static/")
}

// flushCounts drains the in-memory counters into the store. On failure the
// drained deltas are added back so a transient database error loses nothing.
func (s *Server) flushCounts(ctx context.Context) error {
	deltas := make(map[string]int64, len(counterClasses))
	empty := true
	for _, class := range counterClasses {
		if d := s.reqs.bucket(class).Swap(0); d != 0 {
			deltas[class] = d
			empty = false
		}
	}
	if empty {
		return nil
	}
	if err := s.Store.AddRequestCounts(ctx, deltas); err != nil {
		for class, d := range deltas {
			s.reqs.bucket(class).Add(d)
		}
		return err
	}
	return nil
}

// RunCounterFlush persists the request counters on interval until ctx is done,
// then flushes once more so a clean shutdown does not drop the final window.
func (s *Server) RunCounterFlush(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.flushCounts(flushCtx); err != nil {
				log.Printf("api: final request-count flush: %v", err)
			}
			return
		case <-t.C:
			if err := s.flushCounts(ctx); err != nil {
				log.Printf("api: request-count flush: %v", err)
			}
		}
	}
}
