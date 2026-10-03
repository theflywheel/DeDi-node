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
	for _, c := range []struct{ path, query, refused string }{
		// The report's own examples, and the near-misses beside them.
		{"/dedi/query/flywheel/participants", "asOn=2020-01-01T00:00:00Z", "asOn"},
		{"/dedi/query/flywheel/participants", "State=revoked", "State"},
		{"/dedi/query/flywheel", "pageSize=100", "pageSize"},
		{"/dedi/query/flywheel/participants", "page=1&page=2", "page"},
		// The discovery branch reads only domain; a page sent with it was dropped.
		{"/dedi/query/flywheel/participants", "domain=flywheel.in&page=2", "page"},
		{"/dedi/versions/flywheel", "version_id=1", "version_id"},
		{"/dedi/versions/flywheel/participants/bap.example.com", "include_revoked=true", "include_revoked"},
	} {
		m := getJSON(t, srv.URL+c.path+"?"+c.query, http.StatusBadRequest)
		// The refused key opens the message, quoted. Anywhere else would be
		// satisfied by the list of accepted keys that follows it.
		e := fmt.Sprint(m["error"])
		if !strings.HasPrefix(e, fmt.Sprintf("unknown query parameter %q", c.refused)) &&
			!strings.HasPrefix(e, fmt.Sprintf("query parameter %q given more than once", c.refused)) {
			t.Errorf("%s?%s: the refusal does not open by naming %q: %s", c.path, c.query, c.refused, e)
		}
	}
	// Unparseable, and an empty domain: refused, and not as an unknown key.
	for _, c := range []struct{ path, query, says string }{
		{"/dedi/query/flywheel", "status=active;", "malformed"},
		{"/dedi/query/flywheel/participants", "domain=", "domain must not be empty"},
	} {
		m := getJSON(t, srv.URL+c.path+"?"+c.query, http.StatusBadRequest)
		if e := fmt.Sprint(m["error"]); !strings.Contains(e, c.says) {
			t.Errorf("%s?%s: %s, want it to say %q", c.path, c.query, e, c.says)
		}
	}
}

// Every key a real caller sends still works.
//
// Not taken from the allow-lists. From callers: index.html and admin.html
// (page, page_size, domain), the docs (page_size, domain, internal), and a
// CREST spike script (/dedi/versions with no query). The rest (name, status,
// state, sort, from, to, as_on) come from the two specs' parameter lists, not
// from any caller found.
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
