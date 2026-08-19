package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The two pages that are both about witness evidence must reference each other.
//
// /network draws the ring and re-checks an edge; /verify shows every hash and
// byte the verdict rests on. They answer the same question at two depths, and
// until now neither mentioned the other — /verify carried no internal links at
// all beyond its nav. A reader who wanted the layer beneath a green tick had no
// way to learn it existed.
func TestTheTwoEvidencePagesReferenceEachOther(t *testing.T) {
	srv, _, _ := testServer(t)

	get := func(path string) string {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if !strings.Contains(get("/verify"), `href="/network"`) {
		t.Error("/verify never points at the ring it is the evidence for")
	}
	if !strings.Contains(get("/network"), `href="/verify"`) {
		t.Error("/network never points at the page showing what a verdict rests on")
	}
}
