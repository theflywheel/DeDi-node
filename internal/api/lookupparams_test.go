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
			if !strings.Contains(fmt.Sprint(m["error"]), bad) {
				t.Errorf("%s?%s: the error does not name the parameter it refused: %v", path, bad, m["error"])
			}
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
	// An unknown *value* of a known key is still a version that does not exist,
	// not a malformed request; see errNoSuchVersion.
	getJSON(t, base+"?version_id=notanumber", http.StatusNotFound)
}
