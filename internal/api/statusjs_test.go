package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The page's whole thesis is that an old checkpoint over an unchanged log is
// correct, and a write above the last signature is not.
//
// DediCheckpointStale paged someone for seventy-two hours against a healthy
// idle cluster because it read the clock. The chart was built to read log
// POSITION instead — and then the card row went on colouring by age anyway,
// which is the same bug rebuilt one element to the left. These assertions run
// the shipped page's own JS over both shapes and check that only one of them
// alarms.
func TestStatusTellsQuietApartFromBroken(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile(filepath.Join("static", "status.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	// The FIRST <script>, not the last: this page mentions "<script>" inside a
	// comment further down, and taking the last occurrence extracted that
	// sentence instead of the program. The repo already has a test for the same
	// trap around its JSON block.
	js := src[strings.Index(src, "<script>")+len("<script>"):]
	js = js[:strings.Index(js, "</script>")]
	// Drop the bootstrap so nothing fetches on import; the assertions call the
	// functions directly.
	if i := strings.Index(js, "(async function(){"); i >= 0 {
		js = js[:i]
	}
	for _, need := range []string{"function cards(", "function render("} {
		if !strings.Contains(js, need) {
			t.Fatalf("%s is not in the shipped page; it or this harness has moved", need)
		}
	}

	// Quiet: everything written long ago, all of it signed.
	quiet := map[string]any{
		"bucket_seconds": 1800, "signed_through": 100, "signs_here": true,
		"checkpoint_interval_seconds": 60,
		"buckets": []map[string]any{
			{"at": "2026-08-20T00:00:00Z", "entries": 10, "checkpoints": 1, "max_seq": 100},
			{"at": "2026-08-20T00:30:00Z", "entries": 0, "checkpoints": 0},
			{"at": "2026-08-20T01:00:00Z", "entries": 0, "checkpoints": 0},
		},
	}
	// Broken: a write sits above the last signature.
	broken := map[string]any{
		"bucket_seconds": 1800, "signed_through": 100, "signs_here": true,
		"checkpoint_interval_seconds": 60,
		"buckets": []map[string]any{
			{"at": "2026-08-20T00:00:00Z", "entries": 10, "checkpoints": 1, "max_seq": 100},
			{"at": "2026-08-20T00:30:00Z", "entries": 5, "checkpoints": 0, "max_seq": 105},
		},
	}
	q, _ := json.Marshal(quiet)
	b, _ := json.Marshal(broken)

	dir := t.TempDir()
	write := func(n string, b []byte) string {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	qp, bp := write("quiet.json", q), write("broken.json", b)

	harness := `
import fs from 'node:fs';
const els = new Map();
const mk = id => ({ id, innerHTML: '', textContent: '', style: {}, className: '',
  appendChild(){}, title: '' });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, mk(id)); return els.get(id); },
  createElement: () => mk('cell'), querySelector: () => null, addEventListener(){},
};
globalThis.location = { origin: 'http://node.example' };
globalThis.addEventListener = () => {};
globalThis.fetch = async () => ({ ok: true, text: async () => '', json: async () => ({data:{}}) });
const QUIET = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const BROKEN = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
`

	checks := `
let fail = 0;
const ck = (n, c, d) => { if(!c){ console.log('FAIL: ' + n + (d ? ' — ' + d : '')); fail++; } };

// A very old checkpoint, which is the whole point: age must not decide.
const oldMetrics = { dedi_checkpoint_age_seconds: 90000, dedi_cluster_enabled: 0 };

const qShape = render(QUIET);
const qCards = cards(QUIET, qShape, oldMetrics, undefined);
ck('a quiet window must not report a fault', !/writes above the last signature/.test(qCards), qCards.slice(0,200));
ck('a quiet window keeps the age card out of the fault class', !/class="v no"/.test(qCards.split('witness loop')[0]));
ck('a quiet window says so under the chart', /nothing was published, and nothing is wrong/.test(document.getElementById('quiet').innerHTML));
ck('a quiet window reports the signing cadence', /signing every/.test(qCards));

const bShape = render(BROKEN);
const bCards = cards(BROKEN, bShape, oldMetrics, undefined);
ck('an unsigned write must report a fault', /writes above the last signature/.test(bCards), bCards.slice(0,200));
ck('an unsigned write colours the age card as a fault', /class="v no"/.test(bCards.split('witness loop')[0]));
ck('an unsigned write does not print the quiet reassurance', !/nothing is wrong/.test(document.getElementById('quiet').innerHTML));

// A follower must not claim a cadence it does not run.
const follower = Object.assign({}, QUIET, { signs_here: false });
delete follower.checkpoint_interval_seconds;
const fCards = cards(follower, render(follower), oldMetrics, undefined);
ck('a follower does not claim to sign', /leader signs/.test(fCards) && !/signing every/.test(fCards));

// The witness card's four states must read differently.
const st = h => cards(QUIET, qShape, oldMetrics, h);
const words = [
  st({ witnessing: false, nodes: [] }),
  st({ witnessing: true, nodes: [], witness_health: { standby: true } }),
  st({ witnessing: true, nodes: [], witness_health: { stale: true, last_error: 'dial tcp: refused' } }),
  st({ witnessing: true, nodes: [], witness_health: { last_success_at: 'x', seconds_since_success: 21 } }),
].map(h => (h.match(/witness loop<\/div><div class="v [a-z]*">([^<]*)/) || [])[1]);
ck('the four witness states read differently', new Set(words).size === 4, JSON.stringify(words));
ck('a stalled witness is not the same word as a standby one', words[1] !== words[2]);

// Peers: none configured is not the same fact as none answering.
const noPeers = st({ witnessing: false, nodes: [{ self: true, reachable: true }] });
ck('no peers configured says so', /no peers configured/.test(noPeers), noPeers.slice(-300));
const somePeers = st({ witnessing: false, nodes: [
  { self: true, reachable: true }, { name:'b', reachable: true, checked_at: '2026-08-20T13:00:00Z' },
  { name:'c', reachable: false, checked_at: '2026-08-20T13:00:00Z' }] });
ck('peers answering is counted without this node', /1 \/ 2/.test(somePeers), somePeers.slice(-300));
ck('the peers card names whose polls these are', /peers answering this node/.test(somePeers));

// Replica lag: unreplicated is not zero lag.
ck('an unreplicated node says n/a, not 0', /not replicated/.test(qCards) && !/0 entries/.test(qCards));
const clustered = cards(QUIET, qShape,
  { dedi_checkpoint_age_seconds: 10, dedi_cluster_enabled: 1, dedi_cluster_lag_entries: 0,
    dedi_cluster_last_contact_seconds: -1 }, undefined);
ck('never having heard from a leader is not a fresh zero', /never heard from a leader/.test(clustered));

if (fail) process.exit(1);
console.log('STATUS-JS-OK');
`
	run := filepath.Join(dir, "run.mjs")
	if err := os.WriteFile(run, []byte(harness+js+checks), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", run, qp, bp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "STATUS-JS-OK") {
		t.Fatalf("status page JS: %v\n%s", err, out)
	}
}

// The page must not claim a memory the node does not have.
//
// The design canvas draws a "what this node noticed" table — warnings,
// recoveries, peers going unreachable — with timestamps. Nothing in this system
// records that. The store has nine tables and none of them is an event log;
// witness Health keeps only the latest attempt and its last error; the network
// monitor's snapshot is the current observation per peer, so a peer that
// dropped and recovered between two page loads leaves no trace anywhere.
//
// A table built in the browser from what it can poll right now would put
// timestamps on transitions nobody observed, and would lose everything on
// reload — on the page whose own banner says nothing here survives a restart.
// So the table is deferred to a change that persists the events server-side,
// and this stops a browser-side imitation being added in the meantime.
func TestStatusDoesNotInventAHistoryItCannotHave(t *testing.T) {
	srv, _, _ := writeServer(t, "flywheel")
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	for _, claim := range []string{
		"what this node noticed", "noticed", "recovered at", "went unreachable",
		"event history", "since the last restart we",
	} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(claim)) {
			t.Errorf("the page says %q, which implies it remembers past events — "+
				"nothing in the store records them", claim)
		}
	}
	// And the sentence that makes the limit explicit must survive.
	if !strings.Contains(page, "survives a") {
		t.Error("the page no longer says that nothing here survives a restart")
	}
}
