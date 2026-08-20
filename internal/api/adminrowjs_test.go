package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The row actions are openers, not executors.
//
// Putting rotate and revoke on every row is the whole point of the redesign —
// an operator who can see the row should not have to open a detail view to
// reach the action the row is about — but it recreates the exact conditions of
// a bug this page already had once: a panel left bound to the record the
// operator had stopped looking at, so a rotation filled in there published one
// participant's key onto another, with a valid signature and a correct
// precondition, and nothing downstream to question it.
//
// The old tests pinned that invariant at the OLD entry point, the detail panel.
// They cannot see the new one.
func TestRowActionsOpenAPanelAndNeverWriteOnTheirOwn(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(page)
	script = script[strings.Index(script, "<script>")+len("<script>"):]
	script = script[:strings.Index(script, "</script>")]
	for _, boot := range []string{
		"show(location.hash.slice(1) || 'list');", "reload();", "loadChildren();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}
	for _, need := range []string{"function prefill(", "function prefillRevoke(", "function paintParticipants("} {
		if !strings.Contains(script, need) {
			t.Fatalf("%s is not in the shipped page; the console or this harness has moved", need)
		}
	}

	harness := `
const els = new Map();
const mk = id => ({ id, innerHTML: '', textContent: '', value: id === 'ns' ? 'beckn' : '',
  hidden: false, className: '', addEventListener(){}, querySelectorAll(){ return []; },
  scrollIntoView(){}, classList: { toggle(){}, add(){}, remove(){} } });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, mk(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [],
  addEventListener(){},
};
globalThis.location = { hash: '', origin: 'http://node.example' };
globalThis.addEventListener = () => {};
// Any request at all is a failure for this test: opening a panel must not
// write, and must not even ask.
let REQUESTS = 0;
globalThis.fetch = (...a) => { REQUESTS++; return Promise.resolve({ ok: true, json: async () => ({data:{}}) }); };

SCRIPT_HERE

await (async () => {
// Two participants, one of them revoked.
PARTS = [
  { name: 'alice', state: 'live',    det: { subscriber_id: 'alice.example', type: 'BAP', domain: 'mobility' }, ver: '1' },
  { name: 'bob',   state: 'revoked', det: { subscriber_id: 'bob.example',   type: 'BPP', domain: 'retail'   }, ver: '4' },
];
paintParticipants();
const table = document.getElementById('participants').innerHTML;

// 1. Both actions are reachable from the row itself.
for (const act of ['rotate', 'revoke']) {
  if (!table.includes('data-action="' + act + '"')) {
    console.log('FAIL: no ' + act + ' action on the row\n' + table); process.exit(1);
  }
}
// 2. No inline handler. Record names are untrusted and this page holds the key.
if (/onclick=/i.test(table)) { console.log('FAIL: inline handler in a row\n' + table); process.exit(1); }

// 3. A revoked row is distinguishable with the colour removed.
if (!/pill-bad[^>]*>[^<]*revoked/.test(table.replace(/&#10007;/g, ''))) {
  console.log('FAIL: the revoked row does not carry the word\n' + table); process.exit(1);
}

// 4. Opening rotate for alice, then for bob, must not carry alice's values on
//    to bob — and must not write anything on the way.
REQUESTS = 0;
prefill('alice');
document.getElementById('r-sign').value = 'ALICE-KEY';
document.getElementById('r-from').value = '2026-01-01T00:00:00Z';
prefill('bob');
if (document.getElementById('r-sign').value !== '') {
  console.log('FAIL: bob\'s rotate panel still holds alice\'s signing key'); process.exit(1);
}
if (document.getElementById('r-rec').value !== 'bob' ||
    document.getElementById('r-rec-name').textContent !== 'bob') {
  console.log('FAIL: the panel is not bound to bob'); process.exit(1);
}
if (REQUESTS !== 0) { console.log('FAIL: opening a panel issued ' + REQUESTS + ' request(s)'); process.exit(1); }

// 5. The same for revoke, and the panel must be the one on screen — opening
//    revoke closes rotate, so an operator cannot fill one and submit the other.
prefillRevoke('bob');
if (document.getElementById('view-revoke').hidden) { console.log('FAIL: revoke panel not shown'); process.exit(1); }
if (!document.getElementById('view-key').hidden)  { console.log('FAIL: rotate panel left open beside revoke'); process.exit(1); }
if (document.getElementById('v-rec-name').textContent !== 'bob') {
  console.log('FAIL: the revoke panel is not bound to bob'); process.exit(1);
}
if (REQUESTS !== 0) { console.log('FAIL: opening revoke issued ' + REQUESTS + ' request(s)'); process.exit(1); }

// 6. The filter is display-only and never hides what a row IS. A revoked
//    participant that vanished under a filter would be indistinguishable from
//    one that never existed.
document.getElementById('p-filter').value = 'bob';
paintParticipants();
const filtered = document.getElementById('participants').innerHTML;
if (!filtered.includes('bob.example')) { console.log('FAIL: filter dropped a matching row'); process.exit(1); }
if (filtered.includes('alice.example')) { console.log('FAIL: filter did not narrow'); process.exit(1); }
if (!/revoked/.test(filtered)) { console.log('FAIL: the filtered revoked row lost its state'); process.exit(1); }
document.getElementById('p-filter').value = '';
paintParticipants();
if (!document.getElementById('participants').innerHTML.includes('alice.example')) {
  console.log('FAIL: clearing the filter did not restore the list'); process.exit(1);
}

console.log('ROW-ACTIONS-OK');
})();
`
	dir := t.TempDir()
	run := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(run, []byte(strings.Replace(harness, "SCRIPT_HERE", script, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", run).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ROW-ACTIONS-OK") {
		t.Fatalf("row actions: %v\n%s", err, out)
	}
}
