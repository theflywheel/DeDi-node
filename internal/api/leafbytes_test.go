package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theflywheel/DeDi-node/internal/merkle"
)

// The browser's leaf preimage must be byte-identical to the node's.
//
// internal/merkle.LeafBytes builds it with Go's encoding/json, which escapes
// <, > and & in strings by default (and U+2028/U+2029). JSON.stringify escapes
// none of them, and the store accepts all of them in a namespace, registry,
// record name or author — so a valid inclusion proof for a record named
// "a&b.example" folded to a different root in the browser and was reported as
// not being in the log. The verifier calling an honest node a liar is the worst
// failure it has available.
//
// This runs both implementations over the same inputs and compares bytes,
// rather than asserting that either matches a value written down here: a
// hardcoded expectation would drift with whichever side changed.
func TestBrowserLeafPreimageMatchesTheNodes(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	at, err := time.Parse(time.RFC3339Nano, "2026-08-19T10:11:12.5Z")
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		Name    string `json:"name"`
		Type    string `json:"entry_type"`
		NS      string `json:"namespace"`
		Reg     string `json:"registry"`
		Rec     string `json:"record_name"`
		Ver     int32  `json:"version_num"`
		Digest  string `json:"digest"`
		By      string `json:"created_by"`
		At      string `json:"created_at"`
		WantHex string `json:"want"`
	}

	cases := []struct{ ns, reg, rec, by string }{
		{"flywheel", "participants", "plain.example", "seed"},
		// The three Go escapes, one per field that can carry them.
		{"flywheel", "participants", "a&b.example", "seed"},
		{"flywheel", "participants", "x<y", "seed"},
		{"flywheel", "reg>istry", "p.example", "seed"},
		{"ns&amp", "participants", "p.example", "seed"},
		{"flywheel", "participants", "p.example", "op<1>"},
		// Non-ASCII, to be sure nothing else diverges.
		{"flywheel", "participants", "café.example", "señor"},
		{"flywheel", "participants", " line", "seed"},
	}

	var rows []row
	for _, c := range cases {
		want := merkle.LeafBytes("record", c.ns, c.reg, c.rec, 7, []byte{0xab, 0xcd}, c.by, at)
		rows = append(rows, row{
			Name: c.rec, Type: "record", NS: c.ns, Reg: c.reg, Rec: c.rec, Ver: 7,
			Digest: "abcd", By: c.by, At: at.UTC().Format(time.RFC3339Nano),
			WantHex: string(want),
		})
	}
	fixtures, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}

	vjs, err := os.ReadFile(filepath.Join("static", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fixFile := filepath.Join(dir, "cases.json")
	if err := os.WriteFile(fixFile, fixtures, 0o600); err != nil {
		t.Fatal(err)
	}

	script := `
import fs from 'node:fs';
if (!globalThis.crypto) Object.defineProperty(globalThis, 'crypto',
  { value: (await import('node:crypto')).webcrypto, configurable: true });
` + string(vjs) + `
const cases = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const dec = new TextDecoder();
let bad = 0;
for (const c of cases) {
  const got = dec.decode(leafBytes(c));
  if (got !== c.want) {
    bad++;
    console.log('MISMATCH for ' + c.name + '\n  js: ' + got + '\n  go: ' + c.want);
  }
}
console.log(bad === 0 ? 'OK' : 'FAILURES ' + bad);
`
	file := filepath.Join(dir, "leaf.mjs")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", file, fixFile).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("the browser and the node disagree on the leaf preimage:\n%s", out)
	}
}
