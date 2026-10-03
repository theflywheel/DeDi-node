package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

// A bare date, the format the spec gives as_on, must be accepted and must
// mean the whole day (#69).
//
// The expectation is the plain reading of "as on 1 March": anything written on
// 1 March is part of what the record said that day. Reading the date as its
// first instant would answer with the day before.
func TestAsOnADateMeansTheEndOfThatDay(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, _, rec2 := seedBasic(t, s) // rec2 is the latest version, written just now
	day := rec2.CreatedAt.UTC().Format(time.DateOnly)
	base := srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com"

	m := getJSON(t, base+"?as_on="+day, http.StatusOK)
	if got := m["data"].(map[string]any)["version"]; got != versionID(rec2.Seq) {
		t.Errorf("as_on=%s (the day version %d was written) answered version %v", day, rec2.Seq, got)
	}
	// The day before it existed, there was nothing.
	before := rec2.CreatedAt.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	getJSON(t, base+"?as_on="+before, http.StatusNotFound)
	// RFC 3339 keeps working for the callers already sending it.
	getJSON(t, base+"?as_on="+rec2.CreatedAt.UTC().Format(time.RFC3339Nano), http.StatusOK)
	// The refusal names both forms it would have taken.
	m = getJSON(t, base+"?as_on=1%20March", http.StatusBadRequest)
	if e, _ := m["error"].(string); !strings.Contains(e, "YYYY-MM-DD") || !strings.Contains(e, "RFC 3339") {
		t.Errorf("a malformed as_on is refused without naming both accepted forms: %q", e)
	}
}

// from and to on /dedi/query take the same dates, as a closed range of days.
func TestQueryFromAndToTakeDatesAsWholeDays(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, _, rec2 := seedBasic(t, s)
	day := rec2.CreatedAt.UTC().Format(time.DateOnly)
	next := rec2.CreatedAt.UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	q := srv.URL + "/dedi/query/flywheel/participants"

	count := func(url string) int {
		m := getJSON(t, url, http.StatusOK)
		rs, _ := m["data"].(map[string]any)["records"].([]any)
		return len(rs)
	}
	if n := count(q + "?from=" + day + "&to=" + day); n != 1 {
		t.Errorf("from=to=%s (the day it was written) finds %d records, want 1", day, n)
	}
	if n := count(q + "?from=" + next); n != 0 {
		t.Errorf("from=%s (the day after) finds %d records, want 0", next, n)
	}
	// as_on on a query is an upper bound too: the day it was written includes it.
	if n := count(q + "?as_on=" + day); n != 1 {
		t.Errorf("query as_on=%s (the day it was written) finds %d records, want 1", day, n)
	}
}

// A date that has not finished is not history (#69 review).
//
// as_on=<today> is read as the last microsecond of today, which is still
// ahead, and the next write changes the answer. It was cached as immutable
// for a year and skipped the revocation gate, the two things that keep a
// revoked participant from being resolved as live.
func TestAnAsOnThatHasNotHappenedYetIsTreatedAsNow(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	long := now.Add(-72 * time.Hour) // the record existed well before the settled read below
	must := func(in store.AppendInput) {
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	must(store.AppendInput{EntryType: "namespace", Namespace: "flywheel", PayloadRaw: []byte(`{"description":"d"}`), CreatedBy: "t", CreatedAt: long})
	must(store.AppendInput{EntryType: "registry", Namespace: "flywheel", Registry: "participants", PayloadRaw: []byte(`{"description":"r","schema":{"type":"object"}}`), CreatedBy: "t", CreatedAt: long})
	must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bpp", PayloadRaw: []byte(`{"k":"v1"}`), CreatedBy: "t", CreatedAt: long})
	base := srv.URL + "/dedi/lookup/flywheel/participants/bpp"
	settled := now.Add(-48 * time.Hour).Format(time.DateOnly) // ended at least a day ago
	today := now.Format(time.DateOnly)

	cache := func(q string) string {
		resp, err := http.Get(base + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", q, resp.StatusCode)
		}
		return resp.Header.Get("Cache-Control")
	}
	if c := cache("?as_on=" + settled); !strings.Contains(c, "immutable") {
		t.Errorf("as_on=%s is over and its answer cannot change, but is cached %q", settled, c)
	}
	for _, q := range []string{"?as_on=" + today, "?as_on=2100-01-01T00:00:00Z"} {
		if c := cache(q); strings.Contains(c, "immutable") {
			t.Errorf("%s has not happened yet and the next write changes it, but is cached %q", q, c)
		}
	}

	// Revoke it now. "As on today" is a question about now, so it must not
	// resolve the participant; the settled day still answers, as history.
	must(store.AppendInput{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bpp", PayloadRaw: []byte(`{"k":"v1"}`), State: "revoked", CreatedBy: "t", CreatedAt: now})
	getJSON(t, base+"?as_on="+today, http.StatusNotFound)
	getJSON(t, base+"?as_on="+settled, http.StatusOK)
	getJSON(t, base+"?as_on="+today+"&include_revoked=true", http.StatusOK)
}

// A settled as_on that lands on a revoked version answers it, as history.
//
// This is what lets a verifier check a signature made last week by a key
// revoked last week. The test above revokes "now", so its settled read lands
// on a live version and would pass even if every as_on of a revoked record
// were gated.
func TestASettledAsOnStillAnswersARevokedVersion(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, in := range []store.AppendInput{
		{EntryType: "namespace", Namespace: "flywheel", PayloadRaw: []byte(`{"description":"d"}`), CreatedAt: now.Add(-200 * time.Hour)},
		{EntryType: "registry", Namespace: "flywheel", Registry: "participants", PayloadRaw: []byte(`{"description":"r","schema":{"type":"object"}}`), CreatedAt: now.Add(-200 * time.Hour)},
		{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bpp", PayloadRaw: []byte(`{"k":"v1"}`), CreatedAt: now.Add(-200 * time.Hour)},
		{EntryType: "record", Namespace: "flywheel", Registry: "participants", RecordName: "bpp", PayloadRaw: []byte(`{"k":"v1"}`), State: "revoked", CreatedAt: now.Add(-100 * time.Hour)},
	} {
		in.CreatedBy = "t"
		if _, err := s.Append(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	day := now.Add(-48 * time.Hour).Format(time.DateOnly) // after the revocation, and over
	m := getJSON(t, srv.URL+"/dedi/lookup/flywheel/participants/bpp?as_on="+day, http.StatusOK)
	if st := m["data"].(map[string]any)["state"]; st != "revoked" {
		t.Errorf("as_on=%s answered state %v, want the revoked version", day, st)
	}
}
