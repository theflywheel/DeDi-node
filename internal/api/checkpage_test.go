package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The checker must actually verify, and must actually refuse.
//
// Run against a real proof this node produced, with the page's own JS executed
// under node — the same harness convention the other JS tests here use. A page
// that renders a green tick without folding the path would pass any test that
// only looked at the markup.
func TestCheckerVerifiesARealProofAndRejectsATamperedOne(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	srv, s, vkey := testServer(t)
	_, _, rec1, _ := seedBasic(t, s)
	rec1Version := strconv.FormatInt(rec1.Seq, 10)

	// A real record with a real inclusion proof.
	resp, err := http.Get(srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com?proof=inclusion")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lookup: %d — %s", resp.StatusCode, body)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env["proof"] == nil {
		t.Fatalf("no proof in the lookup response: %s", body)
	}

	page, err := os.ReadFile(filepath.Join("static", "check.html"))
	if err != nil {
		t.Fatal(err)
	}
	vjs, err := os.ReadFile(filepath.Join("static", "verify.js"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(page)
	pageJS := full[strings.LastIndex(full, "<script>")+len("<script>"):]
	pageJS = pageJS[:strings.Index(pageJS, "</script>")]
	// The two handler bindings need elements that exist; the harness supplies them.
	good, _ := json.Marshal(env)

	// A HISTORICAL version must verify too. The bind briefly compared
	// data.version_count (the total) to leaf.version_num (this entry's), which
	// are equal only when you look at the newest version — so every honest
	// proof for an older one was rejected. seedBasic writes two versions, so
	// the first is genuinely historical.
	histResp, err := http.Get(srv.URL + "/dedi/lookup/flywheel/participants/bap.example.com?version_id=" +
		rec1Version + "&proof=inclusion")
	if err != nil {
		t.Fatal(err)
	}
	histBody, _ := io.ReadAll(histResp.Body)
	histResp.Body.Close()
	if histResp.StatusCode != http.StatusOK {
		t.Fatalf("historical lookup: %d — %s", histResp.StatusCode, histBody)
	}

	// Splice: a genuine leaf and proof beside somebody else's record. This is
	// the attack the fold cannot see, because everything it checks is true —
	// of a different entry.
	var spliced map[string]any
	json.Unmarshal(body, &spliced)
	sd := spliced["data"].(map[string]any)
	sd["record_name"] = "attacker.example"
	sd["digest"] = strings.Repeat("f", 64)
	splicedJSON, _ := json.Marshal(spliced)

	// Tamper: flip the leaf's digest. The record still parses, and the path is
	// untouched — only the leaf hash changes, so the fold must stop matching.
	var bad map[string]any
	json.Unmarshal(body, &bad)
	leaf := bad["proof"].(map[string]any)["leaf"].(map[string]any)
	dig, _ := leaf["digest"].(string)
	leaf["digest"] = strings.Repeat("0", len(dig))
	badJSON, _ := json.Marshal(bad)

	// The fixtures go in files, not into the script. Embedding marshalled JSON
	// in a template literal turns its \n escapes into real newlines and the
	// document stops being JSON — which is how the first run of this test
	// "failed to verify" a proof that was perfectly good.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.json"), good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), badJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spliced.json"), splicedJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hist.json"), histBody, 0o600); err != nil {
		t.Fatal(err)
	}

	harness := `
import fs from 'node:fs';
const els = new Map();
const fake = id => ({ id, innerHTML: '', value: '', textContent: '', onclick: null,
  addEventListener(){}, });
globalThis.document = { getElementById: id => { if(!els.has(id)) els.set(id, fake(id)); return els.get(id); } };
globalThis.location = { origin: 'http://node.example' };
// Node 22 exposes globalThis.crypto as a getter-only property, so a plain
// assignment throws before any assertion runs. defineProperty works on both,
// and CI runs a newer Node than this laptop — which is why the test passed
// locally and failed there.
if (!globalThis.crypto) {
  Object.defineProperty(globalThis, 'crypto', {
    value: (await import('node:crypto')).webcrypto, configurable: true,
  });
}
globalThis.fetch = async () => ({ ok: true, text: async () => '' });
const GOOD = fs.readFileSync(process.argv[2], 'utf8');
const BAD  = fs.readFileSync(process.argv[3], 'utf8');
const SPLICED = fs.readFileSync(process.argv[4], 'utf8');
const HIST = fs.readFileSync(process.argv[5], 'utf8');
const VKEY = process.argv[6];
`
	check := `
const out = document.getElementById('out');

document.getElementById('doc').value = GOOD;
document.getElementById('vkey').value = VKEY;
await check();
const okHTML = out.innerHTML;
if (!okHTML.includes('is in the log')) throw new Error('a real proof did not verify: ' + okHTML.slice(0, 400));
if (!okHTML.includes('signed by that key')) throw new Error('the signature was not checked: ' + okHTML.slice(0, 400));

// A matching path with a WRONG key must not carry a green headline. The
// verdict used to key on the fold alone, so a failed signature sat under
// "\u2713 This record is in the log" — the false green this page exists to
// avoid, in the page written to avoid it.
document.getElementById('doc').value = GOOD;
document.getElementById('vkey').value = 'other.example/log+11111111+AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
await check();
const wrongKey = out.innerHTML;
if (wrongKey.includes('\u2713 This record is in the log')) {
  throw new Error('a bad signature still produced a green verdict: ' + wrongKey.slice(0, 300));
}

// And with NO key the headline must not claim more than was established.
document.getElementById('doc').value = GOOD;
document.getElementById('vkey').value = '';
await check();
const noKey = out.innerHTML;
if (noKey.includes('\u2713 This record is in the log')) {
  throw new Error('an unsigned checkpoint produced a green verdict: ' + noKey.slice(0, 300));
}
if (!noKey.includes('Partly checked')) {
  throw new Error('no-key case does not say what is missing: ' + noKey.slice(0, 300));
}

// A real proof beside the wrong record must not verify it.
document.getElementById('vkey').value = VKEY;
document.getElementById('doc').value = SPLICED;
await check();
const sp = out.innerHTML;
if (sp.includes('\u2713 This record is in the log')) {
  throw new Error('a proof about a DIFFERENT record verified the one displayed: ' + sp.slice(0, 300));
}
if (!sp.includes('not about this record')) {
  throw new Error('the splice was not reported: ' + sp.slice(0, 300));
}

// An honest proof for an OLDER version must verify. This is the false failure,
// which is quieter than a false pass and just as wrong: a reader checking a
// historical record is told it is not in the log.
document.getElementById('doc').value = HIST;
document.getElementById('vkey').value = VKEY;
await check();
const hi = out.innerHTML;
if (!hi.includes('\u2713 The record shown is the one the proof covers')) {
  throw new Error('an honest historical proof failed the bind: ' + hi.slice(0, 300));
}

// An envelope may not move the record to a log position the proof does not
// cover. proofRoot's base case returned the leaf for ANY index in a size-1
// tree, so a single-entry proof could claim leaf_index 999 — and with
// data.version edited to match, the bind passed and the fold still reached the
// signed root. A green verdict for a position nothing proves.
{
  const env = JSON.parse(GOOD);
  env.proof.leaf_index = 999999;
  env.data.version = '999999';
  document.getElementById('doc').value = JSON.stringify(env);
  document.getElementById('vkey').value = VKEY;
  await check();
  const ix = out.innerHTML;
  if (ix.includes('\u2713 This record is in the log')) {
    throw new Error('an out-of-range leaf index still verified: ' + ix.slice(0, 300));
  }
  if (!ix.includes('not in the tree')) {
    throw new Error('the bad index was not reported as its own step: ' + ix.slice(0, 300));
  }
}

// A proof claiming a different tree size than the checkpoint signed is not
// bound to that checkpoint, however well the root happens to compare.
{
  const env = JSON.parse(GOOD);
  env.proof.tree_size = env.proof.tree_size + 1;
  document.getElementById('doc').value = JSON.stringify(env);
  document.getElementById('vkey').value = VKEY;
  await check();
  const sz = out.innerHTML;
  if (sz.includes('\u2713 This record is in the log')) {
    throw new Error('a proof unbound from the signed size verified: ' + sz.slice(0, 300));
  }
  // The above passes even without the size check, because tree_size feeds the
  // fold and a wrong one already breaks the root comparison. So assert the
  // size step itself is reported — otherwise this test would be vacuous, which
  // it was on its first run.
  if (!sz.includes('does NOT bind to the size')) {
    throw new Error('the size mismatch was not reported as its own step: ' + sz.slice(0, 400));
  }
}

// Deleting a field is easier than forging one. An envelope that simply omits
// the digest — the value that ties the record to the leaf — must not collect a
// passing bind by having nothing to compare.
// namespace is carried twice (namespace_id and namespace) and the bind falls
// back between them, so removing one is not an omission — it takes both.
// Every field the leaf commits to and the response displays must be bound.
// The author and the moment are both on show, so altering either while the
// rest matched used to collect a green bind.
for (const [field, value] of [['created_by', 'publisher:someone-else'],
                              ['updated_at', '2001-01-01T00:00:00Z'],
                              ['version', 999]]) {
  const env = JSON.parse(GOOD);
  env.data[field] = value;
  document.getElementById('doc').value = JSON.stringify(env);
  document.getElementById('vkey').value = VKEY;
  await check();
  const alt = out.innerHTML;
  if (alt.includes('\u2713 The record shown is the one the proof covers')) {
    throw new Error('altering data.' + field + ' still passed the bind: ' + alt.slice(0, 260));
  }
}

for (const fields of [['digest'], ['record_name'], ['namespace_id', 'namespace']]) {
  const field = fields.join('+');
  const env = JSON.parse(GOOD);
  for (const f of fields) delete env.data[f];
  document.getElementById('doc').value = JSON.stringify(env);
  document.getElementById('vkey').value = VKEY;
  await check();
  const om = out.innerHTML;
  if (om.includes('\u2713 The record shown is the one the proof covers')) {
    throw new Error('omitting data.' + field + ' still passed the bind: ' + om.slice(0, 260));
  }
  if (om.includes('\u2713 This record is in the log')) {
    throw new Error('omitting data.' + field + ' still produced a green verdict');
  }
}

// An envelope with a proof and no record binds to nothing, and must not report
// that the record shown matches — there is no record shown.
{
  const env = JSON.parse(GOOD);
  delete env.data;
  document.getElementById('doc').value = JSON.stringify(env);
  await check();
  const nd = out.innerHTML;
  if (nd.includes('The record shown is the one the proof covers')) {
    throw new Error('an envelope with no record claimed a matching bind: ' + nd.slice(0, 300));
  }
}

document.getElementById('vkey').value = VKEY;
document.getElementById('doc').value = BAD;
await check();
const badHTML = out.innerHTML;
if (badHTML.includes('\u2713 This record is in the log')) {
  throw new Error('a TAMPERED record verified — the fold is not being computed');
}
if (!badHTML.includes('does NOT fold')) throw new Error('the tamper was not reported: ' + badHTML.slice(0, 400));
console.log('OK');
`
	file := filepath.Join(dir, "check.mjs")
	src := harness + string(vjs) + "\n" + pageJS + "\n" + check
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", file,
		filepath.Join(dir, "good.json"), filepath.Join(dir, "bad.json"),
		filepath.Join(dir, "spliced.json"), filepath.Join(dir, "hist.json"), vkey).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("checker: %v\n%s", err, out)
	}
}
