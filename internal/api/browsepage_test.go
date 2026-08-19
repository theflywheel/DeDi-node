package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// /browse is the directory browser and nothing else.
//
// When "/" was split into an overview and a browser, the network panel was
// given its own page — and left behind on the browser as well. Both rendered
// the same data from different code, and /browse polled /dedi/network every 15
// seconds for a panel that had a page of its own. The split fixed two thirds of
// "one page doing three jobs" and quietly kept the third.
//
// Two copies of a rendering is the drift the shared verifier comment warns
// about: the one that is wrong still shows ticks.
func TestBrowseDoesNotDuplicateTheNetworkPage(t *testing.T) {
	srv, _, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/browse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	for _, gone := range []string{"loadNetwork", "clusterBlock", "loadReplication", "verifyWitness"} {
		if strings.Contains(page, gone) {
			t.Errorf("/browse still carries %s, which now lives on /network", gone)
		}
	}
	if strings.Contains(page, `id="network"`) {
		t.Error("/browse still renders the network panel")
	}
	// And it must say where that went, rather than simply losing it.
	if !strings.Contains(page, `href="/network"`) {
		t.Error("/browse does not point at the network page")
	}
	// What it is actually for must still be there.
	if !strings.Contains(page, "Browse the directory") {
		t.Error("/browse lost the browser")
	}
}
