package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// The node's own past: what was written, what was signed, and what that means.
//
// A gap in a checkpoint history is ambiguous on its own. internal/checkpoint
// deliberately signs nothing when the tree has not grown, so a gap may be a
// stalled signer or simply a quiet log — and publishing the checkpoints alone
// invites every reader to resolve that the alarming way. That is precisely what
// our own Prometheus rule does, and why it has been firing on a healthy idle
// cluster for days.
//
// So both series come back together, on one grid, from one query, with the one
// extra fact that makes a fault decidable: the highest log position reached in
// each bucket. A checkpoint covers a PREFIX of the log, so an entry at or below
// the latest checkpoint's tree size is signed no matter when either happened.
// Deciding it by whether a checkpoint landed in the same time bucket compares
// two clocks against a bucket boundary and is wrong routinely — steady-state
// signing is one tick after the write it covers, which is often the next
// bucket.
//
// Three states fall out, and only the third is a fault:
//
//	checkpoints in the bucket                  → signing
//	no entries and no checkpoints              → idle: nothing to sign, by design
//	entries above the signed prefix            → writes that nothing has signed
//
// On /dedi/ with the rest of the read plane, because it is evidence about the
// log and a monitor should not have to scrape HTML for it.
func (s *Server) logHistory(w http.ResponseWriter, r *http.Request) {
	buckets := 48
	if v := r.URL.Query().Get("buckets"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			buckets = n
		}
	}
	bucket := 30 * time.Minute
	if v := r.URL.Query().Get("bucket_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 60 {
			bucket = time.Duration(n) * time.Second
		}
	}
	rows, err := s.Store.History(r.Context(), buckets, bucket)
	if err != nil {
		internal(w, err)
		return
	}

	// The signed prefix. Entries at or below this are covered by a signature;
	// anything above it is genuinely unsigned, whatever the clock says.
	var signedThrough int64 = -1
	if size, _, err := s.Store.LatestCheckpointAt(r.Context()); err == nil {
		signedThrough = size
	} else if !errors.Is(err, store.ErrNoCheckpoint) {
		internal(w, err)
		return
	}

	out := make([]map[string]any, 0, len(rows))
	var entries, signed int64
	for _, b := range rows {
		entries += b.Entries
		signed += b.Checkpoints
		m := map[string]any{
			"at": fmtTime(b.At), "checkpoints": b.Checkpoints, "entries": b.Entries,
		}
		if b.MaxSeq >= 0 {
			m["max_seq"] = b.MaxSeq
		}
		out = append(out, m)
	}

	data := map[string]any{
		"buckets":        out,
		"bucket_seconds": int64(bucket.Seconds()),
		"entries":        entries,
		"checkpoints":    signed,
		// -1 means this node has never signed anything, which is a different
		// fact from "signed up to entry 0" and must stay distinguishable.
		"signed_through": signedThrough,
	}
	// Who signed these. On a Raft follower the answer is not "this node": it
	// never signs at all, and every checkpoint row here is the leader's
	// signature applied locally. A page that says "what this node signed" is
	// simply wrong on two of every three replicas, so the fact travels with the
	// data rather than being left for the page to assume.
	if s.Cluster != nil {
		st := s.Cluster()
		data["cluster_enabled"] = true
		data["signs_here"] = st.Role == "leader"
		data["role"] = st.Role
	} else {
		data["cluster_enabled"] = false
		data["signs_here"] = true
	}
	ok(w, "History retrieved successfully", data)
}

// statusPage serves the operational view of this node's own past.
func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	page := statusPageHTML
	page = replaceOnce(page, "{{STATUS_URL}}", s.externalStatusHref())
	s.writePage(w, page, "/status")
}
