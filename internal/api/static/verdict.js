// One reading of one witness verdict, shared by every page that draws one.
//
// A verdict has more than two answers. It can be caught, sound, never
// attempted, or — the one that keeps getting lost — present but not stating a
// result. Each page that drew a verdict used to test only for the failure case,
// `consistency_ok === false`, so anything else fell through to the affirmative
// branch: a solid arrow, a tick, a green "verified" over a bit nobody had
// asserted.
//
// That reading is not hypothetical. These verdicts are fetched cross-origin
// from peers of arbitrary version, and omitting a JSON field is what an older
// build does by accident.
//
// This file exists because the rule was written down once — in a comment on the
// overview page, warning that "any reader who treats a missing field as a pass
// turns 'never checked' into 'checked and fine'" — and the other pages never
// got it. Five pages had the bug before this was extracted. A sixth copy of the
// test is how there would be a sixth.
//
// Callers must branch on the state, never re-derive it from the fields.

// hasHealth reports whether a verdict carries a liveness block that can be read.
//
// Truthiness is not enough and the distinction is not academic: a peer sending
// health as a string satisfies `if (t.health)` and then reports neither stale
// nor checking, which lands on the affirmative branch. The classifier and the
// caveat must ask the identical question or the page will show one and mean the
// other.
function hasHealth(t) {
  return !!t && typeof t.health === 'object' && t.health !== null && !Array.isArray(t.health);
}

// witnessStale reports a witness loop that has stopped or fallen behind.
// Absent health is NOT stale — it is unknown, which hasHealth handles above.
function witnessStale(t) {
  if (!hasHealth(t)) return false;
  const h = t.health;
  if (h.standby) return false;            // a follower is not witnessing on purpose
  return h.stale === true || h.checking === false;
}

// verdictState classifies one verdict. Every value it can return names a
// different thing a reader must be told, and only 'sound' may be drawn
// affirmatively.
function verdictState(t) {
  if (!t) return 'none';
  if (t.consistency_ok === false) return 'caught';
  if (t.witnessed !== true) return 'never';
  // The affirmative value, not merely the absence of the negative one.
  if (t.consistency_ok !== true) return 'unstated';
  // A verdict that names no tree size states nothing checkable. Rendering it
  // anyway printed "✓ NaN" under a green tick — a string the overview page's
  // own comment records as a bug it already fixed once.
  if (typeof t.size !== 'number' || !Number.isFinite(t.size)) return 'unsized';
  if (!hasHealth(t)) return 'unwatched';
  if (witnessStale(t)) return 'stale';
  return 'sound';
}

// num renders a quantity, or an em dash when the node did not send one.
//
// Number(undefined) is NaN and toLocaleString renders it "NaN", so a field the
// node never sent printed as a quantity under that field's own label. Four
// pages carried the same unguarded one-liner, so the same defect was in all
// four at once; it lives here now so the guard cannot be true of three of
// them.
function num(n) {
  if (n === null || n === undefined || n === '') return '\u2014';
  const v = Number(n);
  return Number.isFinite(v) ? v.toLocaleString() : '\u2014';
}

// writesUnsigned reports entries in the log above the newest signed tree size:
// writes no checkpoint covers yet. Arguments are the dedi_log_entries and
// dedi_checkpoint_tree_size metrics; a missing reading is not evidence of
// anything, so it answers false.
//
// This, not the checkpoint's age, is what says the signer is behind. The
// checkpointer signs only when the tree grows, so on a quiet log an old
// checkpoint is correct — the DediWritesUnsigned alert rule and the status
// page both decide by position for that reason, and so must every page.
function writesUnsigned(entries, signedSize) {
  return Number.isFinite(entries) && Number.isFinite(signedSize) && entries > signedSize;
}

if (typeof module !== 'undefined') {
  module.exports = { hasHealth, witnessStale, verdictState, num, writesUnsigned };
}
