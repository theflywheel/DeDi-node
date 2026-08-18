package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Rotate and revoke open against the participant selected in the table. That
// binding is the whole point of moving them out of their own tabs — and it is
// also the one way this rearrangement could do real damage, because the panel
// now sits on a page where another participant is still one click away.
//
// If selecting a different participant leaves the panel open, the operator sees
// a form headed "rotating B" bound to A. Whatever they submit is signed against
// A, with a valid signature and a correct precondition, so nothing downstream
// has any reason to question it. Before the restructure the form was a full
// view and no other participant was clickable while it was open.
//
// Run against admin.html as shipped, like the other JS tests here: a copy is
// the drift being guarded against.
func TestRotatePanelClosesWhenTheSelectionChanges(t *testing.T) {
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
		"setMode();", "show(location.hash.slice(1) || 'list');", "reload();", "loadChildren();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}
	for _, fn := range []string{"function prefill(", "function detail(", "function hideLifecycle("} {
		if !strings.Contains(script, fn) {
			t.Fatalf("%s is not in the shipped page; the panel or this harness has moved", fn)
		}
	}

	harness := `
const els = new Map();
const fakeEl = id => ({ id, innerHTML: '', textContent: '', value: id === 'ns' ? 'beckn' : '',
  hidden: false, className: '', addEventListener(){}, querySelectorAll(){ return []; },
  scrollIntoView(){} });
globalThis.document = {
  getElementById: id => { if(!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  querySelector: () => null, querySelectorAll: () => [],
};
globalThis.location = { hash: '', origin: 'http://node.example' };
globalThis.addEventListener = () => {};
globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({ data: {} }) });
`

	check := `
// Select A and open the rotate panel against it.
prefill('participant-a');
if (document.getElementById('view-key').hidden) throw new Error('rotate panel did not open');
if (document.getElementById('r-rec').value !== 'participant-a') throw new Error('record not bound');

// Now select a different participant, exactly as clicking another row does.
detail('participant-b');

if (!document.getElementById('view-key').hidden) {
  throw new Error('WRONG-RECORD: the rotate panel is still open after selecting participant-b, ' +
    'bound to ' + document.getElementById('r-rec').value);
}

// Reopening must not carry the previous participant's key or validity window:
// the panel is reused in place now, and a form headed "rotating B" holding A's
// signing key would publish A's key onto B.
document.getElementById('r-sign').value = 'AAAA-previous-key';
document.getElementById('r-from').value = '2020-01-01T00:00:00Z';
prefill('participant-b');
if (document.getElementById('r-sign').value !== '') throw new Error('STALE: previous signing key survived');
if (document.getElementById('r-from').value !== '') throw new Error('STALE: previous validity window survived');
if (document.getElementById('r-rec').value !== 'participant-b') throw new Error('record not rebound');

// Revoke behaves the same way.
prefillRevoke('participant-a');
if (document.getElementById('view-revoke').hidden) throw new Error('revoke panel did not open');
if (!document.getElementById('view-key').hidden) throw new Error('both panels open at once');
detail('participant-b');
if (!document.getElementById('view-revoke').hidden) throw new Error('WRONG-RECORD: revoke panel survived');

console.log('OK');
`

	dir := t.TempDir()
	file := filepath.Join(dir, "check.mjs")
	if err := os.WriteFile(file, []byte(harness+script+check), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", file).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Fatalf("unexpected output: %s", out)
	}
}
