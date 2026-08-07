package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The child-nodes panel renders strings a *child* supplied — its origin, its
// URL, its verifier key — into the operator console. That console is the one
// page in the system holding a publisher key in memory, so markup injected
// through an enrolment would be executing next to it.
//
// A child is a separate operator by construction. Treating what it sends as
// trusted because it holds a delegation gets the direction of trust exactly
// backwards: the delegation is what this node grants, not what it receives.
//
// Tested against admin.html as shipped rather than a copy, for the reason
// verifyjs_test gives: a copy is the drift being guarded against.
func TestChildPanelEscapesWhatAChildSupplied(t *testing.T) {
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
	// Drop only the bootstrap lines. Truncating the file at them would also
	// drop the child-panel functions, which are defined after — and the test
	// would then fail on a missing symbol while looking like an escaping bug.
	for _, boot := range []string{
		"show(location.hash.slice(1) || 'list');", "reload();", "loadChildren();",
	} {
		script = strings.Replace(script, boot, "", 1)
	}
	if !strings.Contains(script, "async function loadChildren") {
		t.Fatal("loadChildren is not in the shipped page; the panel or this harness has moved")
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

// A child that enrolled with markup in every field it controls.
const hostile = {
  namespace: 'beckn.evil',
  label: '<img src=x onerror=alert(1)>',
  state: 'active',
  child_origin: '"><script>alert(2)</` + `script>',
  child_url: 'javascript:alert(3)',
  child_key: '<svg onload=alert(4)>',
  enrolled_at: '<b>now</b>',
};
globalThis.fetch = () => Promise.resolve({
  ok: true, json: () => Promise.resolve({ data: { children: [hostile] } }),
});

SCRIPT_HERE

await (async () => {
await loadChildren();
const html = document.getElementById('children-list').innerHTML;
const leaks = ['<img', '<script', '<svg', '<b>now'].filter(t => html.includes(t));
// An href is not made safe by escaping: javascript: survives esc() intact and
// runs on click, in the page holding the publisher key.
if (/href="javascript:/i.test(html)) {
  console.log('LEAKED javascript: href\n' + html);
  process.exit(1);
}
if (leaks.length) {
  console.log('LEAKED ' + JSON.stringify(leaks) + '\n' + html);
  process.exit(1);
}
// And it must actually have rendered the row, or this passes vacuously.
if (!html.includes('beckn.evil')) { console.log('NO ROW RENDERED\n' + html); process.exit(1); }
console.log('OK');
})();
`
	prog := strings.Replace(harness, "SCRIPT_HERE", script, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "child_panel.mjs")
	if err := os.WriteFile(path, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("child panel does not escape child-supplied values:\n%s\n%v", out, err)
	}
}
