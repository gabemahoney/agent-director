package apitest

import "strconv"

// descriptions_unusable.go holds the shared description helper's cases for a
// row whose recorded tmux session name is unusable (SR-1.4, SR-1.7, SR-3.2;
// Epic 19): the three ErrInternal descriptions every verb's refusal gives
// (DescUnusableNameEmpty, DescUnusableNameControlChar,
// DescUnusableNameRewritten), the full trigger sentence a Client method's Go
// doc prose and kill's manifest Description state (DescUnusableNameTrigger),
// and the short pointer the other verbs' manifest Descriptions carry
// (DescUnusableNamePointer), and the find-missing and expire result fields
// that report such a row (DescUnusableNameFindMissingField,
// DescUnusableNameExpireField). None may tell the caller to delete the row
// (SR-18.8, SR-18.17 step 5).

// unusableNameMustNot is what no unusable-name refusal or pointer may say:
// that the row is to be deleted (SR-18.8: only "Operator actions" does).
var unusableNameMustNot = []string{"delete"}

// unusableName is an unusable recorded name's ErrInternal case (SR-1.4,
// SR-3.2): req, then that the name cannot be used, so removing the row is a
// human's decision, the "Operator actions" pointer, and that no tmux call
// was made; never "delete", nor any of mustNot.
func unusableName(kind string, req, mustNot []string) DescCase {
	return DescCase{
		Name: "ErrInternal, recorded name " + kind,
		Require: append(req,
			"the name cannot be used, so removing the row is a human's decision", "no tmux call was made"),
		MustNot: append(append([]string(nil), unusableNameMustNot...), mustNot...),
	}.PointsToOperatorActions()
}

// DescUnusableNameEmpty is ErrInternal for a row whose recorded tmux session
// name is empty.
func DescUnusableNameEmpty() DescCase {
	return unusableName("empty", []string{"the recorded tmux session name is empty"}, nil)
}

// DescUnusableNameControlChar is ErrInternal for a recorded tmux session name
// with a control character, quoted (Go quoted-string form).
func DescUnusableNameControlChar(name string) DescCase {
	return unusableName("with a control character",
		[]string{strconv.Quote(name), "the recorded tmux session name contains a control character"}, nil)
}

// RewrittenChars says which characters tmux stores differently a recorded
// name holds: '.', ':', bytes that are not valid UTF-8.
type RewrittenChars struct {
	Dot         bool
	Colon       bool
	InvalidUTF8 bool
}

// DescUnusableNameRewritten is ErrInternal for a recorded tmux session name
// holding a character tmux stores differently: the quoted name and which
// (each of which's set; each unset one must not be named).
func DescUnusableNameRewritten(name string, which RewrittenChars) DescCase {
	req := []string{strconv.Quote(name), "the recorded tmux session name contains a character tmux stores differently"}
	var mustNot []string
	for _, ch := range []struct {
		set    bool
		phrase string
	}{{which.Dot, "'.'"}, {which.Colon, "':'"}, {which.InvalidUTF8, "bytes that are not valid UTF-8"}} {
		if ch.set {
			req = append(req, ch.phrase)
		} else {
			mustNot = append(mustNot, ch.phrase)
		}
	}
	return unusableName("tmux rewrites", req, mustNot)
}

// DescUnusableNameTrigger is the unusable recorded-name ErrInternal trigger
// in full, as kill's manifest Description and each looking-up verb's Client
// Go doc prose state it (SR-1.7, SR-3.2): the name's three kinds (empty, a
// control character, a character tmux stores differently), ErrInternal with
// no tmux call, and removing the row a human's decision with the "Operator
// actions" pointer.
func DescUnusableNameTrigger() DescCase {
	return DescCase{
		Name: "unusable recorded-name ErrInternal trigger",
		Require: []string{
			"recorded tmux session name cannot be used",
			"it is empty, contains a control character, or contains a character tmux stores differently",
			"gets ErrInternal", "with no tmux call", "removing the row is a human's decision",
		},
	}.PointsToOperatorActions()
}

// DescUnusableNamePointer is the short form of the trigger that the read-pane,
// send-keys, pause, resume and spawn manifest Descriptions carry (SR-1.7;
// Epic 19 decision 1): ErrInternal, an unusable recorded name (either case)
// and "(see kill)", where the full sentence is; never "delete". Check it with
// AssertAgentTextCase, which adds the session-ending and opt-in forms.
func DescUnusableNamePointer() DescCase {
	return DescCase{
		Name:        "manifest, unusable recorded-name pointer",
		Require:     []string{"ErrInternal", "recorded name", "(see kill)"},
		MustNot:     unusableNameMustNot,
		requireFold: []string{"unusable"},
	}
}

// unusableNameKinds is the three kinds as the sweeps' result fields list them.
const unusableNameKinds = "empty, a control character, or a character tmux stores differently"

// unusableNameFieldMustNot is ErrInternal (neither sweep has it) and any
// instruction to delete the row; plain "delete" passes ("a failed delete").
var unusableNameFieldMustNot = []string{
	"ErrInternal", "`delete`", "delete the row", "delete such a row", "delete it",
	"run delete", "call delete", "use delete",
}

// unusableNameField is a sweep result field's unusable-name case: the
// recorded name cannot be used, and req.
func unusableNameField(name string, req ...string) DescCase {
	return DescCase{
		Name:    name + ", unusable recorded name",
		Require: append([]string{"recorded tmux session name cannot be used"}, req...),
		MustNot: unusableNameFieldMustNot,
	}
}

// DescUnusableNameFindMissingField is find-missing's unverified_ids on such a
// row (SR-11.3, SR-18.11): its kinds, no tmux call, a note of its own, a
// human's decision and the "Operator actions" pointer. Check it with
// AssertAgentTextCase, beside DescFindMissingField.
func DescUnusableNameFindMissingField() DescCase {
	return unusableNameField("find-missing result field, unverified_ids",
		unusableNameKinds, "no tmux call", "a liveness note of its own", "a human's decision",
	).PointsToOperatorActions()
}

// DescUnusableNameExpireField is expire's kept or kept_ids on such a row
// (SR-12.2, SR-18.11). kept: kept on every run, before any other check, with
// no tmux call. kept_ids: also its kinds, a reason of its own, a human's
// decision and the "Operator actions" pointer. Check it with
// AssertAgentTextCase, beside DescExpireField; it panics on another field.
func DescUnusableNameExpireField(f ExpireField) DescCase {
	kept := []string{"kept on every run", "before any other check", "no tmux call"}
	switch f {
	case ExpireKept:
		return unusableNameField("expire result field, kept", kept...)
	case ExpireKeptIDs:
		return unusableNameField("expire result field, kept_ids",
			append(kept, unusableNameKinds, "a reason of its own", "a human's decision")...,
		).PointsToOperatorActions()
	}
	panic("apitest: DescUnusableNameExpireField: not a kept field of expire: " + string(f))
}
