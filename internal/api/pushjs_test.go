package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The push panel renders a target URL the operator typed and an id this node
// generated — neither obviously hostile. But the same page holds the publisher
// key in memory, and a target URL is the one field here that is *meant* to be a
// URL, which makes the javascript: case easy to wave through: it is a valid
// URL, esc() leaves it entirely intact, and it runs on click.
//
// Tested against admin.html as shipped rather than a copy, for the reason
// verifyjs_test gives: a copy is the drift being guarded against.
func TestPushPanelEscapesWhatItRenders(t *testing.T) {
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
		"loadSubscriptions();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}
	if !strings.Contains(script, "async function loadSubscriptions") {
		t.Fatal("loadSubscriptions is not in the shipped page; the panel or this harness has moved")
	}

	harness := `
const els = new Map();
const fakeEl = id => ({ id, innerHTML: '', textContent: '', value: id === 'ns' ? 'beckn' : '',
  hidden: false, className: '', addEventListener(){}, querySelectorAll(){ return []; } });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [],
};
globalThis.location = { hash: '', origin: 'http://node.example' };
globalThis.addEventListener = () => {};
globalThis.crypto = { subtle: {
  digest: async () => new Uint8Array(32),
  sign: async () => new Uint8Array(64),
} };

const hostile = {
  // The id is carried into a data- attribute by the unsubscribe link, so the
  // quote that would close the attribute has to be neutralised too.
  id: 'sub_1" onmouseover="alert(1)',
  namespace: 'beckn',
  registry: '<img src=x onerror=alert(2)>',
  // A valid URL that esc() leaves completely intact and that runs on click, in
  // the one page holding a publisher key.
  target_url: 'javascript:alert(3)',
  state: 'active',
  pending: '<b>7</b>',
  dead_lettered: '<svg onload=alert(4)>',
  retrying: 0,
  last_error: '<script>alert(5)</` + `script>',
};
globalThis.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve({ data: {
    subscriptions: [hostile],
    delivery: { running: true, leader: true, last_sweep: '<i>now</i>' },
  } }),
});

SCRIPT_HERE

await (async () => {
SIGNER = { kid: 'op-1', key: {} };
await loadSubscriptions();
const html = document.getElementById('push-list').innerHTML +
             document.getElementById('push-health').innerHTML;
const leaks = ['<img', '<script', '<svg', '<b>7', '<i>now'].filter(t => html.includes(t));
if (/href="javascript:/i.test(html)) {
  console.log('LEAKED javascript: href\n' + html);
  process.exit(1);
}
if (leaks.length) {
  console.log('LEAKED ' + JSON.stringify(leaks) + '\n' + html);
  process.exit(1);
}
if (/onmouseover="/i.test(html)) {
  console.log('LEAKED attribute break out of a data- attribute\n' + html);
  process.exit(1);
}
// And it must actually have rendered, or this passes vacuously.
if (!html.includes('data-action="unsub"')) { console.log('NO ROW RENDERED\n' + html); process.exit(1); }
// The dead-letter count is the one an operator must not miss: it means a
// consumer was never told about a change and may still trust a withdrawn key.
if (!html.includes('given up on')) { console.log('DEAD LETTERS NOT SHOWN\n' + html); process.exit(1); }
console.log('OK');
})();
`
	prog := strings.Replace(harness, "SCRIPT_HERE", script, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "push_panel.mjs")
	if err := os.WriteFile(path, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("push panel does not escape what it renders:\n%s\n%v", out, err)
	}
}

// The loop's health and the queues must not be able to stand in for each
// other. A dead loop with quiet queues is the failure this separation exists to
// make visible, so the page has to say so in words rather than leave the
// operator to infer it from rows that read "up to date".
func TestPushPanelSaysWhenTheLoopIsNotRunning(t *testing.T) {
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
		"loadSubscriptions();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}

	harness := `
const els = new Map();
const fakeEl = id => ({ id, innerHTML: '', textContent: '', value: id === 'ns' ? 'beckn' : '',
  hidden: false, className: '', addEventListener(){}, querySelectorAll(){ return []; } });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [],
};
globalThis.location = { hash: '', origin: 'http://node.example' };
globalThis.addEventListener = () => {};
globalThis.crypto = { subtle: { digest: async () => new Uint8Array(32), sign: async () => new Uint8Array(64) } };
globalThis.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve({ data: {
    // A queue that looks perfectly healthy behind a loop that is not running.
    subscriptions: [{ id: 'sub_1', namespace: 'beckn', registry: 'subscribers',
                      target_url: 'https://consumer.example/hook', state: 'active',
                      pending: 0, dead_lettered: 0 }],
    delivery: { running: false, leader: false, last_sweep: '' },
  } }),
});

SCRIPT_HERE

await (async () => {
SIGNER = { kid: 'op-1', key: {} };
await loadSubscriptions();
const health = document.getElementById('push-health').innerHTML;
if (!/not running/i.test(health)) {
  console.log('A DEAD LOOP IS NOT REPORTED\n' + health);
  process.exit(1);
}
// The row still reads "up to date", which is exactly why the loop has to speak
// for itself — this asserts the trap is present, not that it was fixed by
// changing the row.
const list = document.getElementById('push-list').innerHTML;
if (!/up to date/.test(list)) { console.log('EXPECTED A HEALTHY-LOOKING ROW\n' + list); process.exit(1); }
console.log('OK');
})();
`
	prog := strings.Replace(harness, "SCRIPT_HERE", script, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "push_health.mjs")
	if err := os.WriteFile(path, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("push panel does not report a stopped delivery loop:\n%s\n%v", out, err)
	}
}

// The domain panel renders a URL each participant chose for itself. It is the
// same trap as the child table's child_url: a valid URL that esc() leaves
// completely intact and that runs on click, in the one page holding a
// publisher key.
func TestDomainPanelDoesNotTrustAParticipantSuppliedURL(t *testing.T) {
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
		"loadSubscriptions();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}
	if !strings.Contains(script, "async function findByDomain") {
		t.Fatal("findByDomain is not in the shipped page; the panel or this harness has moved")
	}

	harness := `
const els = new Map();
const fakeEl = id => ({ id, innerHTML: '', textContent: '',
  value: id === 'ns' ? 'beckn' : (id === 'd-reg' ? 'subscribers' : 'retail'),
  hidden: false, className: '', addEventListener(){}, querySelectorAll(){ return []; } });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [],
};
globalThis.location = { hash: '', origin: 'http://node.example' };
globalThis.addEventListener = () => {};
globalThis.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ data: {
  participants: [{
    subscriber_id: '<img src=x onerror=alert(1)>',
    type: '<svg onload=alert(2)>',
    url: 'javascript:alert(3)',
    lookup_url: '/dedi/lookup/beckn/subscribers/x',
    record_name: 'x',
  }],
} }) });

SCRIPT_HERE

await (async () => {
await findByDomain();
const html = document.getElementById('domain-list').innerHTML;
if (/href="javascript:/i.test(html)) { console.log('LEAKED javascript: href\n' + html); process.exit(1); }
const leaks = ['<img', '<svg', '<script'].filter(t => html.includes(t));
if (leaks.length) { console.log('LEAKED ' + JSON.stringify(leaks) + '\n' + html); process.exit(1); }
// It must have rendered, and it must point at the record rather than asking
// the operator to believe the list this node assembled.
if (!html.includes('>verify<')) { console.log('NO VERIFY LINK\n' + html); process.exit(1); }
console.log('OK');
})();
`
	prog := strings.Replace(harness, "SCRIPT_HERE", script, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "domain_panel.mjs")
	if err := os.WriteFile(path, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("domain panel trusts a participant-supplied URL:\n%s\n%v", out, err)
	}
}
