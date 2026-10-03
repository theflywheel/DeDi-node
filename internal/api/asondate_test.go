package api

import (
	"net/http"
	"testing"
	"time"
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
	// Neither form is a 400 that names only one of them.
	m = getJSON(t, base+"?as_on=1%20March", http.StatusBadRequest)
	if e, _ := m["error"].(string); e == "" {
		t.Error("a malformed as_on gets no explanation")
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
}
