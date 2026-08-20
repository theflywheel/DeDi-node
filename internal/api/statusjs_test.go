package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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

	// The witness fixtures come from the server's own witnessHealth, so the
	// card is tested against what it will really be handed.
	iv := time.Minute
	health, err := json.Marshal([]map[string]any{
		witnessHealth(WitnessState{Standby: true, Interval: iv}),
		witnessHealth(WitnessState{Interval: iv, Attempts: 3, Failures: 3, LastError: "dial tcp: refused"}),
		witnessHealth(WitnessState{Interval: iv, Attempts: 9,
			LastSuccessAt: time.Now().Add(-10 * iv), LastError: "timeout"}),
		witnessHealth(WitnessState{Interval: iv, Attempts: 9, LastSuccessAt: time.Now().Add(-21 * time.Second)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	hp := write("health.json", health)

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
// Pin the CLASS, not its absence.
//
// The first version of this assertion checked only that the age card was not
// in the fault class — and the rule it exists to forbid, age < 3600 ? ok : mut,
// never produces the fault class either. Reinstating that exact rule left this
// test green. The clock rule renders "mut" on an old checkpoint; the position
// rule renders "ok". So the class is the assertion.
const qAge = (qCards.match(/checkpoint age<\/div><div class="v ([a-z]*)"/) || [])[1];
ck('a very old but fully-covered checkpoint reads ok, not muted', qAge === 'ok', 'got class ' + qAge);
ck('a quiet window says so under the chart', /nothing was published, and nothing is wrong/.test(document.getElementById('quiet').innerHTML));
ck('a quiet window reports the signing cadence', /signing every/.test(qCards));

// An unsigned write OUTSIDE the 24h window must still be a fault. shape.unsigned
// only sees the window, so the card also compares the whole log against the
// signed tree size — otherwise it made a lifetime claim from a window
// measurement, green, while the verdict line above correctly said "in this
// window".
const olderUnsigned = cards(QUIET, qShape,
  { dedi_checkpoint_age_seconds: 5, dedi_cluster_enabled: 0,
    dedi_log_entries: 500, dedi_checkpoint_tree_size: 400 }, undefined);
ck('an unsigned write older than the window is still a fault',
   /writes above the last signature/.test(olderUnsigned), olderUnsigned.slice(0, 160));
const allCovered = cards(QUIET, qShape,
  { dedi_checkpoint_age_seconds: 5, dedi_cluster_enabled: 0,
    dedi_log_entries: 400, dedi_checkpoint_tree_size: 400 }, undefined);
ck('a fully-signed log is not a fault', !/writes above the last signature/.test(allCovered));

const bShape = render(BROKEN);
const bCards = cards(BROKEN, bShape, oldMetrics, undefined);
ck('an unsigned write must report a fault', /writes above the last signature/.test(bCards), bCards.slice(0,200));
const bAge = (bCards.match(/checkpoint age<\/div><div class="v ([a-z]*)"/) || [])[1];
ck('an unsigned write colours the age card as a fault', bAge === 'no', 'got class ' + bAge);

// And the class must not move with the clock at all: the same covered window
// at a wildly different age must read the same.
const fresh = cards(QUIET, qShape, { dedi_checkpoint_age_seconds: 5, dedi_cluster_enabled: 0 }, undefined);
const freshAge = (fresh.match(/checkpoint age<\/div><div class="v ([a-z]*)"/) || [])[1];
ck('the age class does not depend on the age', freshAge === qAge, freshAge + ' vs ' + qAge);
ck('an unsigned write does not print the quiet reassurance', !/nothing is wrong/.test(document.getElementById('quiet').innerHTML));

// A follower must not claim a cadence it does not run.
const follower = Object.assign({}, QUIET, { signs_here: false });
delete follower.checkpoint_interval_seconds;
const fCards = cards(follower, render(follower), oldMetrics, undefined);
ck('a follower does not claim to sign', /leader signs/.test(fCards) && !/signing every/.test(fCards));

// The witness card's states must read differently — and the shapes are the
// server's own, not shapes invented here. Hand-written fixtures let the card
// look like it distinguished states that witnessHealth never actually emits:
// it sets stale on a never-succeeded loop too, and the page was testing stale
// first, so "never succeeded" was unreachable in production while the test
// passed on a fixture the server would never send.
const st = h => cards(QUIET, qShape, oldMetrics, h);
const HEALTH = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
const words = HEALTH.map(h => {
  const html = st({ witnessing: true, nodes: [], witness_health: h });
  return (html.match(/witness loop<\/div><div class="v [a-z]*">([^<]*)/) || [])[1];
});
ck('every witness state the server emits reads differently', new Set(words).size === HEALTH.length,
   JSON.stringify(words));
const notWitnessing = (st({ witnessing: false, nodes: [] })
  .match(/witness loop<\/div><div class="v [a-z]*">([^<]*)/) || [])[1];
ck('witnessing nobody is its own word', !words.includes(notWitnessing), notWitnessing);

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
	out, err := exec.Command("node", run, qp, bp, hp).CombinedOutput()
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

// Every metric the page reads must be one the node emits.
//
// A card once branched on dedi_cluster_heartbeat_seconds, which does not
// exist. It degraded quietly — undefined, so the branch never fired — which
// meant the one state that card exists to catch could not be caught, and
// nothing anywhere said so. A page reading a metric by the wrong name fails
// exactly like a page reading nothing.
func TestStatusReadsOnlyMetricsTheNodeEmits(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("static", "status.html"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(page)
	// Names the page actually indexes out of the scrape.
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`m\['(dedi_[a-z_]+)'\]`).FindAllStringSubmatch(src, -1) {
		used[m[1]] = true
	}
	if len(used) == 0 {
		t.Fatal("the page reads no metrics at all, so this proves nothing")
	}
	// Names the binary writes.
	mw, err := os.ReadFile("metrics.go")
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(dedi_[a-z_]+)"`).FindAllStringSubmatch(string(mw), -1) {
		emitted[m[1]] = true
	}
	for name := range used {
		if !emitted[name] {
			t.Errorf("the page branches on %s, which metrics.go never emits — that branch can never run", name)
		}
	}
	// And every name it scrapes should be one it uses; a name fetched and
	// discarded is either dead weight or a card someone forgot to finish.
	scrape := src[strings.Index(src, "for (const name of"):]
	scrape = scrape[:strings.Index(scrape, "const m = body.match")]
	for _, m := range regexp.MustCompile(`'(dedi_[a-z_]+)'`).FindAllStringSubmatch(scrape, -1) {
		if !used[m[1]] {
			t.Errorf("the page scrapes %s and never reads it", m[1])
		}
	}
}
