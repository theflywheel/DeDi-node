package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A misspelt pin must fail, not quietly answer a different question (#65).
//
// ?versionId=2&proof=inclusion returned the latest version with a valid proof:
// indistinguishable from a correct answer to every check a client can run.
func TestALookupRejectsAParameterItDoesNotRead(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, rec1, _ := seedBasic(t, s)
	for _, path := range []string{
		"/dedi/lookup/flywheel",
		"/dedi/lookup/flywheel/participants",
		"/dedi/lookup/flywheel/participants/bap.example.com",
	} {
		// The near-misses a client actually writes, taken from the report.
		for _, bad := range []string{"versionId", "version", "versionid", "asOn"} {
			m := getJSON(t, fmt.Sprintf("%s%s?%s=%d&proof=inclusion", srv.URL, path, bad, rec1.Seq), http.StatusBadRequest)
			// Quoted: the message also lists the accepted keys, and "version"
			// is a substring of "version_id", so a bare match proves nothing.
			if !strings.Contains(fmt.Sprint(m["error"]), fmt.Sprintf("%q", bad)) {
				t.Errorf("%s?%s: the error does not name the parameter it refused: %v", path, bad, m["error"])
			}
		}
		// A query the parser cannot read must not shed the part it chokes on
		// and answer with what is left.
		for _, raw := range []string{
			fmt.Sprintf("proof=inclusion&versionId=%d;", rec1.Seq),
			fmt.Sprintf("version_id=%d;proof=inclusion", rec1.Seq),
			"versionId=%zz&proof=inclusion",
			// Repeated: the first value would win, and here it is "unpinned".
			fmt.Sprintf("version_id=&version_id=%d&proof=inclusion", rec1.Seq),
			"as_on=&as_on=2000-01-01T00:00:00Z",
		} {
			getJSON(t, srv.URL+path+"?"+raw, http.StatusBadRequest)
		}
	}
}

// Strictness must not lock out a caller that is already right.
//
// The keys here are the ones real callers send: CREST's pkg/dedi (version_id,
// proof), ONIX's dediregistry (none), the node's own pages (include_revoked,
// internal). A key missing from the allow-list fails here, not in production.
func TestALookupStillAcceptsEveryParameterACallerSends(t *testing.T) {
	srv, s, _ := testServer(t)
	_, _, rec1, _ := seedBasic(t, s)
	base := srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com"
	for _, q := range []string{
		"",
		fmt.Sprintf("?version_id=%d", rec1.Seq),
		fmt.Sprintf("?version_id=%d&proof=inclusion", rec1.Seq),
		"?as_on=2100-01-01T00:00:00Z",
		"?include_revoked=true",
		"?internal=1",
	} {
		getJSON(t, base+q, http.StatusOK)
	}
	// The refusal must tell a caller every key it could have used.
	m := getJSON(t, base+"?nope=1", http.StatusBadRequest)
	for k := range lookupParams {
		if !strings.Contains(fmt.Sprint(m["error"]), k) {
			t.Errorf("the 400 does not mention accepted key %q: %v", k, m["error"])
		}
	}
	// An unknown *value* of a known key is still a version that does not exist,
	// not a malformed request; see errNoSuchVersion.
	getJSON(t, base+"?version_id=notanumber", http.StatusNotFound)
}
