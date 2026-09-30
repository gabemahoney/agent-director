package tmux

// This file holds the label classes of the shared lookup: a session's parsed
// label judged against one row (SRD SR-3.4, SR-3.10, SR-3.13; WD 2026-09-29
// STORE). Only the label's parsed fields and the row's Launch are used: never
// a raw value, a session name, the environment or the clock.

// LabelClass is a session label's class relative to one row X, in this store
// (SR-3.4; WD 2026-09-29 STORE). The zero value is not a valid class.
type LabelClass int

// The five label classes of SR-3.4.
const (
	// ClassCurrent: this store's id, X's instance id and X's non-empty launch
	// token. A session carrying it is the row's own Ours session.
	ClassCurrent LabelClass = iota + 1
	// ClassOld: this store's id and X's instance id with another token, or
	// any token when X records none: a session of an earlier launch of X.
	ClassOld
	// ClassForeign: this store's id and another instance id.
	ClassForeign
	// ClassOtherStore: a valid label whose store id is not this store's,
	// whatever its instance id and token: another agent-director store's
	// agent, never Ours or Leftover, and counted toward Gone for the row (WD
	// 2026-09-29 STORE).
	ClassOtherStore
	// ClassNone: no valid label (LabelNone: empty, unparseable, a four-field
	// value, borrowed, or a control character).
	ClassNone
)

// CaseWords returns the words SR-3.10 gives a name holder of this class:
// "left over from an earlier life" (old), "a different instance id"
// (foreign), "another agent-director store" (other store; WD 2026-09-29
// STORE) and "no valid instance id" (none). Current has none, since the
// holder is then the row's own Ours session; an invalid class has none
// either.
func (c LabelClass) CaseWords() string {
	switch c {
	case ClassOld:
		return "left over from an earlier life"
	case ClassForeign:
		return "a different instance id"
	case ClassOtherStore:
		return "another agent-director store"
	case ClassNone:
		return "no valid instance id"
	}
	return ""
}

// ClassOf classes the label lb against the row l (SR-3.4, SR-3.13; WD
// 2026-09-29 STORE). Every comparison is byte for byte:
//
//   - LabelNone is ClassNone.
//   - A valid label whose StoreID is not l.StoreID is ClassOtherStore,
//     whatever its instance id and token. An empty l.StoreID matches no
//     valid label, so every valid label is then another store's (fail
//     closed).
//   - A label naming another instance id is ClassForeign. So is every valid
//     label when l.InstanceID is empty or holds an ASCII control character
//     (0x00-0x1f, 0x7f), so such a row never gets current or old (SR-3.13);
//     the label parser already rejects such ids, and this guards a fake that
//     hands one in.
//   - A label with l's instance id is ClassCurrent when l.Token is not empty
//     and equals the label's token, and ClassOld otherwise: a row with no
//     launch token never has a current label, so it is never Ours.
func (l Launch) ClassOf(lb Label) LabelClass {
	if lb.Kind != LabelValid {
		return ClassNone
	}
	if l.StoreID == "" || lb.StoreID != l.StoreID {
		return ClassOtherStore
	}
	if l.InstanceID == "" || hasControlChar(l.InstanceID) || lb.InstanceID != l.InstanceID {
		return ClassForeign
	}
	if l.Token != "" && lb.Token == l.Token {
		return ClassCurrent
	}
	return ClassOld
}
