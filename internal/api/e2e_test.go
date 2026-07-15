package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

type proofResp struct {
	Data struct {
		Details json.RawMessage `json:"details"`
	} `json:"data"`
	Proof struct {
		LeafIndex  int64    `json:"leaf_index"`
		TreeSize   int64    `json:"tree_size"`
		Checkpoint string   `json:"checkpoint"`
		Path       []string `json:"path"`
		Leaf       struct {
			EntryType  string `json:"entry_type"`
			Namespace  string `json:"namespace"`
			Registry   string `json:"registry"`
			RecordName string `json:"record_name"`
			VersionNum int32  `json:"version_num"`
			Digest     string `json:"digest"`
			CreatedBy  string `json:"created_by"`
			CreatedAt  string `json:"created_at"`
		} `json:"leaf"`
	} `json:"proof"`
}

// TestOfflineProofVerification is the M1 exit criterion: a client with only
// the node's public key verifies a lookup response with no further trust in
// the server.
func TestOfflineProofVerification(t *testing.T) {
	srv, s, vkey := testServer(t)
	seedBasic(t, s)

	resp, err := http.Get(srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com?proof=inclusion")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var pr proofResp
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 1. Checkpoint signature verifies against the node's public key.
	verifier, err := note.NewVerifier(vkey)
	if err != nil {
		t.Fatal(err)
	}
	n, err := note.Open([]byte(pr.Proof.Checkpoint), note.VerifierList(verifier))
	if err != nil {
		t.Fatalf("checkpoint signature: %v", err)
	}
	origin, cpSize, cpRoot, err := merkle.ParseCheckpoint(n.Text)
	if err != nil {
		t.Fatal(err)
	}
	if cpSize != pr.Proof.TreeSize {
		t.Fatalf("checkpoint size %d != proof tree_size %d", cpSize, pr.Proof.TreeSize)
	}
	if origin != "test.dedi.local/log" {
		t.Fatalf("checkpoint origin = %q, want test.dedi.local/log", origin)
	}

	// 1b. Proven leaf identifies the exact resource that was requested.
	if pr.Proof.Leaf.EntryType != "record" || pr.Proof.Leaf.Namespace != "flywheel" ||
		pr.Proof.Leaf.Registry != "participants" || pr.Proof.Leaf.RecordName != "bap.example.com" {
		t.Fatalf("proof leaf identity does not match requested resource: %+v", pr.Proof.Leaf)
	}

	// 2. Served payload bytes hash to the leaf's digest.
	sum := sha256.Sum256(pr.Data.Details)
	if hex.EncodeToString(sum[:]) != pr.Proof.Leaf.Digest {
		t.Fatal("details bytes do not hash to leaf digest")
	}

	// 3. Reconstructed leaf is included under the checkpoint root.
	createdAt, err := time.Parse(time.RFC3339Nano, pr.Proof.Leaf.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString(pr.Proof.Leaf.Digest)
	if err != nil {
		t.Fatal(err)
	}
	leafBytes := merkle.LeafBytes(pr.Proof.Leaf.EntryType, pr.Proof.Leaf.Namespace,
		pr.Proof.Leaf.Registry, pr.Proof.Leaf.RecordName, pr.Proof.Leaf.VersionNum,
		digest, pr.Proof.Leaf.CreatedBy, createdAt)
	proof := make(tlog.RecordProof, len(pr.Proof.Path))
	for i, p := range pr.Proof.Path {
		hb, err := base64.StdEncoding.DecodeString(p)
		if err != nil || len(hb) != 32 {
			t.Fatalf("path[%d] invalid", i)
		}
		copy(proof[i][:], hb)
	}
	if err := tlog.CheckRecord(proof, pr.Proof.TreeSize, cpRoot, pr.Proof.LeafIndex, tlog.RecordHash(leafBytes)); err != nil {
		t.Fatalf("inclusion check failed: %v", err)
	}
}

func TestConsistencyEndpoint(t *testing.T) {
	srv, s, _ := testServer(t)
	seedBasic(t, s) // tree size 4
	m := getJSON(t, srv.URL+"/dedi/log/proof/consistency?old=2&new=4", http.StatusOK)
	data := m["data"].(map[string]any)
	if data["old_size"].(float64) != 2 || data["new_size"].(float64) != 4 {
		t.Fatalf("sizes: %v", data)
	}
	if len(data["proof"].([]any)) == 0 {
		t.Fatal("empty consistency proof")
	}
	mm := getJSON(t, srv.URL+"/dedi/log/proof/consistency?old=4&new=2", http.StatusBadRequest)
	if mm["code"] != "INVALID_REQUEST" {
		t.Fatalf("code: %v", mm["code"])
	}
	getJSON(t, fmt.Sprintf("%s/dedi/log/proof/consistency?old=1&new=%d", srv.URL, 999), http.StatusBadRequest)
}
