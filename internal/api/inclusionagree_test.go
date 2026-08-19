package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// verify.js carries two implementations of inclusion-proof folding: proofRoot,
// which produces the answer, and proofRootTraced, which records every hash
// combination so /verify can show the derivation instead of a verdict. The file
// says they must agree.
//
// A test asserted that for CONSISTENCY proofs and nothing asserted it for
// inclusion, so when an out-of-range index guard was added it went into the
// untraced one only — leaving /verify, which computes its verdict with the
// traced variant, still accepting an index that is not in the tree. The page
// showing every byte was more permissive than the page showing a tick.
//
// This runs both over the same inputs, including the malformed ones, and
// requires identical outcomes. It is the general form: any future guard added
// to one and not the other fails here rather than in whichever page happens to
// call the unguarded copy.
func TestBothInclusionVerifiersAgree(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	vjs, err := os.ReadFile(filepath.Join("static", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}

	script := `
if (!globalThis.crypto) Object.defineProperty(globalThis, 'crypto',
  { value: (await import('node:crypto')).webcrypto, configurable: true });
` + string(vjs) + `
const enc = new TextEncoder();
const leaf = await leafHash(enc.encode('leaf'));
const sib  = await leafHash(enc.encode('sibling'));
const b64 = u => Buffer.from(u).toString('base64');

// (path, treeSize, leafIndex) — honest shapes and every malformed one that has
// mattered here.
const cases = [
  [[],     1, 0],
  [[],     1, 999],     // the size-1 hole: any index used to fold to the leaf
  [[],     1, -1],
  [[],     1, 1.5],
  [[sib],  2, 0],
  [[sib],  2, 1],
  [[sib],  2, 7],
  [[],     2, 0],       // empty path for a tree that needs one
  [[sib],  1, 0],       // path supplied where none belongs
];

let disagreements = 0;
for (const [path, t, n] of cases) {
  const run = async fn => {
    try { return 'ok:' + b64(await fn()); } catch (e) { return 'throw'; }
  };
  const plain  = await run(() => proofRoot(path.slice(), t, n, leaf));
  const traced = await run(() => proofRootTraced(path.slice(), t, n, leaf, []));
  if (plain !== traced) {
    disagreements++;
    console.log('DISAGREE t=' + t + ' n=' + n + ' path=' + path.length +
                '  plain=' + plain.slice(0, 12) + ' traced=' + traced.slice(0, 12));
  }
}
console.log(disagreements === 0 ? 'OK' : 'DISAGREEMENTS ' + disagreements);
`
	dir := t.TempDir()
	file := filepath.Join(dir, "agree.mjs")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", file).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("the two inclusion verifiers disagree:\n%s", out)
	}
}
