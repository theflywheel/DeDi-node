package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/mod/sumdb/tlog"
)

// The proof checking that the explorer and the evidence page rely on is
// JavaScript, and it is the part a reader is asked to trust the most: it decides
// whether a consistency proof holds. A verifier that answered "valid" to
// everything would be worse than no verification at all, and nothing about the
// page would look different.
//
// So it is tested here, from the Go suite, against vectors produced by the same
// library the node itself verifies with. The JavaScript is loaded from
// static/verify.js as shipped, not from a copy, because a copy is exactly the
// drift this is guarding against.

type memHashes struct{ h []tlog.Hash }

func (m *memHashes) ReadHashes(idx []int64) ([]tlog.Hash, error) {
	out := make([]tlog.Hash, len(idx))
	for i, id := range idx {
		if id >= int64(len(m.h)) {
			return nil, fmt.Errorf("hash %d out of range", id)
		}
		out[i] = m.h[id]
	}
	return out, nil
}

type treeVector struct {
	Name  string   `json:"name"`
	T     int64    `json:"t"`
	TH    string   `json:"th"`
	N     int64    `json:"n"`
	H     string   `json:"h"`
	Proof []string `json:"proof"`
	Want  bool     `json:"want"`
}

func b64Hash(h tlog.Hash) string { return base64.StdEncoding.EncodeToString(h[:]) }

func buildVectors(t *testing.T) []treeVector {
	t.Helper()
	m := &memHashes{}
	roots := map[int64]tlog.Hash{}
	for i := int64(0); i < 64; i++ {
		hashes, err := tlog.StoredHashes(i, []byte(fmt.Sprintf("leaf-%d", i)), m)
		if err != nil {
			t.Fatal(err)
		}
		m.h = append(m.h, hashes...)
		root, err := tlog.TreeHash(i+1, m)
		if err != nil {
			t.Fatal(err)
		}
		roots[i+1] = root
	}
	proofFor := func(n, tt int64) []string {
		proof, err := tlog.ProveTree(tt, n, m)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(proof))
		for i, h := range proof {
			out[i] = b64Hash(h)
		}
		return out
	}

	var vectors []treeVector
	// Honest proofs, including the awkward sizes: a single-entry old tree, exact
	// powers of two, and n == t where there is nothing to prove.
	for _, pair := range [][2]int64{{1, 2}, {1, 8}, {3, 5}, {5, 13}, {8, 16}, {7, 7}, {13, 64}, {31, 33}} {
		n, tt := pair[0], pair[1]
		vectors = append(vectors, treeVector{
			Name: fmt.Sprintf("honest %d->%d", n, tt),
			T:    tt, TH: b64Hash(roots[tt]), N: n, H: b64Hash(roots[n]),
			Proof: proofFor(n, tt), Want: true,
		})
	}

	// Forgeries: each is a way a node could try to pass off a rewritten history,
	// and every one must be rejected.
	honest := proofFor(5, 13)
	tampered := append([]string(nil), honest...)
	tampered[0] = b64Hash(roots[1])

	vectors = append(vectors,
		treeVector{Name: "tampered proof hash", T: 13, TH: b64Hash(roots[13]), N: 5,
			H: b64Hash(roots[5]), Proof: tampered, Want: false},
		treeVector{Name: "wrong old root", T: 13, TH: b64Hash(roots[13]), N: 5,
			H: b64Hash(roots[6]), Proof: honest, Want: false},
		treeVector{Name: "wrong new root", T: 13, TH: b64Hash(roots[12]), N: 5,
			H: b64Hash(roots[5]), Proof: honest, Want: false},
		treeVector{Name: "dropped proof hash", T: 13, TH: b64Hash(roots[13]), N: 5,
			H: b64Hash(roots[5]), Proof: honest[:len(honest)-1], Want: false},
		treeVector{Name: "empty proof", T: 13, TH: b64Hash(roots[13]), N: 5,
			H: b64Hash(roots[5]), Proof: nil, Want: false},
		treeVector{Name: "same size, mismatched root", T: 7, TH: b64Hash(roots[7]), N: 7,
			H: b64Hash(roots[6]), Proof: nil, Want: false},
	)
	return vectors
}

func TestBrowserConsistencyVerifierAgreesWithTlog(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping the browser verifier check")
	}

	vectors := buildVectors(t)
	path := filepath.Join(t.TempDir(), "vectors.json")
	encoded, err := json.Marshal(vectors)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	// Paths are relative to this package; the harness and the shipped script both
	// live in the repository.
	out, err := exec.Command(node, "../../scripts/verify_vectors.mjs", path,
		"static/verify.js").CombinedOutput()
	if err != nil {
		t.Fatalf("the browser verifier disagreed with x/mod/sumdb/tlog:\n%s", out)
	}
	t.Logf("\n%s", out)
}
