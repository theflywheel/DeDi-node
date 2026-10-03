package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A listing must refuse a filter it would otherwise ignore (#73).
//
// ?asOn=2020-... listed today's records as an answer about 2020, and ?State=
// listed everything unfiltered: a wrong answer shaped exactly like a right one.
func TestAListingRejectsAParameterItDoesNotRead(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)
	for _, c := range []struct{ path, bad string }{
		// The report's own examples, and the near-misses beside them.
		{"/dedi/query/flywheel/participants", "asOn=2020-01-01T00:00:00Z"},
		{"/dedi/query/flywheel/participants", "State=revoked"},
		{"/dedi/query/flywheel", "pageSize=100"},
		{"/dedi/query/flywheel", "status=active;"}, // unparseable, not ignored
		{"/dedi/query/flywheel/participants", "page=1&page=2"},
		// The discovery branch reads only domain; a page sent with it was dropped.
		{"/dedi/query/flywheel/participants", "domain=flywheel.in&page=2"},
		{"/dedi/versions/flywheel", "version_id=1"},
		{"/dedi/versions/flywheel/participants/bap.example.com", "include_revoked=true"},
	} {
		m := getJSON(t, srv.URL+c.path+"?"+c.bad, http.StatusBadRequest)
		key := strings.SplitN(strings.SplitN(c.bad, "=", 2)[0], "&", 2)[0]
		if e := fmt.Sprint(m["error"]); !strings.Contains(e, key) && !strings.Contains(e, "malformed") {
			t.Errorf("%s?%s: the refusal does not say what it refused: %s", c.path, c.bad, e)
		}
	}
}

// Every key a real caller sends still works.
//
// Taken from the callers, not from the allow-lists: index.html (page,
// page_size), admin.html (page, page_size, domain), the docs and monitors
// (internal, status, state, sort, name), and the spec's own query parameters,
// which conformance/ also exercises.
func TestAListingStillAcceptsEveryParameterACallerSends(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s)
	for _, u := range []string{
		"/dedi/query/flywheel?page=1&page_size=100",
		"/dedi/query/flywheel?status=active&sort=name&name=participants",
		"/dedi/query/flywheel/participants?page_size=100&page=1",
		"/dedi/query/flywheel/participants?state=live&from=2000-01-01&to=2100-01-01&as_on=2100-01-01",
		"/dedi/query/flywheel/participants?domain=flywheel.in",
		"/dedi/query/flywheel?internal=1",
		"/dedi/versions/flywheel",
		"/dedi/versions/flywheel/participants?internal=1",
		"/dedi/versions/flywheel/participants/bap.example.com",
	} {
		resp, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest {
			t.Errorf("%s: 400 for a request a caller already sends", u)
		}
	}
}
