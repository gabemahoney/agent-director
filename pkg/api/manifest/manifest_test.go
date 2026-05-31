package manifest_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestVerbsContainsExpectedSurface pins the canonical verb order. Each
// Epic that adds a verb appends to this slice; the test catches a missing
// manifest entry on the source-of-truth side (the doc-drift gate catches
// it on the reference-doc side). Order matters: the generator walks Verbs
// in slice order, so a reorder produces a diff in docs/cli-reference.md.
func TestVerbsContainsExpectedSurface(t *testing.T) {
	want := []string{"help", "spawn", "status", "get", "send-keys", "read-pane", "kill", "decide", "get-permission", "resume", "find-missing", "expire", "delete", "make-template", "list", "pause", "serve", "version", "hook"}
	if got := len(manifest.Verbs); got != len(want) {
		t.Fatalf("len(manifest.Verbs) = %d, want %d (names %v)", got, len(want), want)
	}
	for i, name := range want {
		if manifest.Verbs[i].Name != name {
			t.Errorf("manifest.Verbs[%d].Name = %q, want %q", i, manifest.Verbs[i].Name, name)
		}
	}
}

// TestSpawnHasAllSRDErrorNames asserts the spawn entry advertises every
// validation / launch error name from SRD §13.1. Doc drift CI catches the
// reference-doc side; this test pins the source-of-truth side.
func TestSpawnHasAllSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	want := []string{
		"ErrCwdMissing", "ErrCwdNotAPath", "ErrCwdNotFound", "ErrCwdNotADirectory",
		"ErrRelayModeInvalid", "ErrSpawnDeniedFlag", "ErrReservedEnvKey",
		"ErrInstanceIdCollision", "ErrTmuxNotAvailable", "ErrTmuxSessionCreate",
	}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("spawn.ErrorNames missing %q", n)
		}
	}
}

// TestSendKeysHasInteractErrorNames pins the send-keys entry's error
// catalog against the SRD §13.1 surface: the state-precondition guard,
// the Epic-10 relay stub, and the two transport-layer tmux sentinels.
func TestSendKeysHasInteractErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("send-keys")
	if !ok {
		t.Fatal("send-keys not in manifest")
	}
	want := []string{
		"ErrSpawnNotFound",
		"ErrSpawnNotInteractive",
		"ErrSendKeysWhileRelayed",
		"ErrTmuxNotAvailable",
		"ErrTmuxSendKeys",
	}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("send-keys.ErrorNames missing %q", n)
		}
	}
}

// TestReadPaneHasInteractErrorNames pins the read-pane entry's error
// catalog against the SRD §13.1 surface: the row-lookup miss and the two
// transport-layer tmux sentinels. read-pane has no state precondition, so
// no ErrSpawnNotInteractive.
func TestReadPaneHasInteractErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("read-pane")
	if !ok {
		t.Fatal("read-pane not in manifest")
	}
	want := []string{
		"ErrSpawnNotFound",
		"ErrTmuxNotAvailable",
		"ErrTmuxCaptureFailed",
	}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("read-pane.ErrorNames missing %q", n)
		}
	}
}

// TestListHasSRDErrorNames pins the list entry's error catalog against
// SRD §13.1: the label k=v parse rejection is the only verb-surface
// error; the verb has no state precondition and no transport-layer tmux.
func TestListHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("list")
	if !ok {
		t.Fatal("list not in manifest")
	}
	want := []string{"ErrListInvalidLabel"}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("list.ErrorNames missing %q", n)
		}
	}
}

// TestKillHasSRDErrorNames pins the kill entry's error catalog against
// SRD §13.1: kill swallows tmux failures and is idempotent on terminal
// states, so the only surface error is the row-lookup miss.
func TestKillHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("kill")
	if !ok {
		t.Fatal("kill not in manifest")
	}
	want := []string{"ErrSpawnNotFound"}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("kill.ErrorNames missing %q", n)
		}
	}
}

// TestPauseHasSRDErrorNames pins the pause entry's error catalog against
// SRD §13.1: state-precondition guard, the poll-timeout sentinel, and the
// two transport-layer tmux sentinels.
func TestPauseHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("pause")
	if !ok {
		t.Fatal("pause not in manifest")
	}
	want := []string{
		"ErrSpawnNotFound",
		"ErrSpawnNotPausable",
		"ErrPauseTimeout",
		"ErrTmuxNotAvailable",
		"ErrTmuxSendKeys",
	}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("pause.ErrorNames missing %q", n)
		}
	}
}

// TestLookup covers the hit and miss paths of Lookup against the real
// registry. No hand-constructed entries.
func TestLookup(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		v, ok := manifest.Lookup("help")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "help")
		}
		if v.Name != "help" {
			t.Fatalf("Lookup(%q).Name = %q, want %q", "help", v.Name, "help")
		}
	})
	t.Run("miss", func(t *testing.T) {
		v, ok := manifest.Lookup("nonexistent")
		if ok {
			t.Fatalf("Lookup(%q) ok = true, want false", "nonexistent")
		}
		if !reflect.DeepEqual(v, manifest.VerbDef{}) {
			t.Fatalf("Lookup miss returned non-zero VerbDef: %+v", v)
		}
	})
}

// TestHelpVerbRequiredFields is a table-driven check that the help entry
// carries every field downstream consumers (CLI dispatch, MCP schema, doc
// generator) expect to be populated.
func TestHelpVerbRequiredFields(t *testing.T) {
	v, ok := manifest.Lookup("help")
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", "help")
	}
	cases := []struct {
		field   string
		nonZero bool
	}{
		{"Name", v.Name != ""},
		{"Description", v.Description != ""},
		{"ResultFields", len(v.ResultFields) > 0},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			if !c.nonZero {
				t.Fatalf("help.%s is empty/zero; want populated", c.field)
			}
		})
	}
}

// TestHelpErrorNamesEmptyNonNil enforces the JSON-stability invariant: help
// has no error conditions, so ErrorNames must marshal as [] not null.
func TestHelpErrorNamesEmptyNonNil(t *testing.T) {
	v, ok := manifest.Lookup("help")
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", "help")
	}
	if v.ErrorNames == nil {
		t.Fatalf("help.ErrorNames is nil; want empty non-nil slice")
	}
	if len(v.ErrorNames) != 0 {
		t.Fatalf("len(help.ErrorNames) = %d, want 0", len(v.ErrorNames))
	}
}

// ── Phase 2: Callable ────────────────────────────────────────────────────────

// TestCallableVerbsCount asserts CallableVerbs() returns exactly 16 entries —
// the full set of synchronous verb methods on *pkg/api.Client.
func TestCallableVerbsCount(t *testing.T) {
	got := manifest.CallableVerbs()
	if len(got) != 16 {
		names := make([]string, len(got))
		for i, v := range got {
			names[i] = v.Name
		}
		t.Fatalf("len(CallableVerbs()) = %d, want 16 (got %v)", len(got), names)
	}
}

// TestCallableVerbsExcludesNonCallable asserts help, serve, and hook are not
// in the callable set.
func TestCallableVerbsExcludesNonCallable(t *testing.T) {
	excluded := []string{"help", "serve", "hook"}
	for _, cv := range manifest.CallableVerbs() {
		for _, name := range excluded {
			if cv.Name == name {
				t.Errorf("CallableVerbs() contains %q; it should be Callable: false", name)
			}
		}
	}
}

// TestCallableVerbsOrder asserts the callable subset preserves Verbs-defined
// order (the 16 callable verbs in the order they appear in Verbs).
func TestCallableVerbsOrder(t *testing.T) {
	want := []string{
		"spawn", "status", "get", "send-keys", "read-pane", "kill",
		"decide", "get-permission", "resume", "find-missing", "expire", "delete",
		"make-template", "list", "pause", "version",
	}
	got := manifest.CallableVerbs()
	if len(got) != len(want) {
		t.Fatalf("len(CallableVerbs()) = %d, want %d", len(got), len(want))
	}
	for i, v := range got {
		if v.Name != want[i] {
			t.Errorf("CallableVerbs()[%d].Name = %q, want %q", i, v.Name, want[i])
		}
	}
}

// TestCallableVerbsIsNewSlice asserts CallableVerbs() returns a fresh slice
// rather than a sub-slice of Verbs (mutations to the result must not affect
// the canonical Verbs slice).
func TestCallableVerbsIsNewSlice(t *testing.T) {
	cv := manifest.CallableVerbs()
	if len(cv) == 0 {
		t.Fatal("CallableVerbs() is empty; cannot test slice identity")
	}
	origName := manifest.Verbs[0].Name
	cv[0].Name = "mutated"
	if manifest.Verbs[0].Name != origName {
		t.Errorf("mutating CallableVerbs()[0].Name changed Verbs[0].Name; slices are aliased")
	}
}

// TestAllVerbsHaveExplicitCallable asserts every entry in Verbs has an
// explicit Callable value aligned with the locked assignment.
func TestAllVerbsHaveExplicitCallable(t *testing.T) {
	nonCallable := map[string]bool{"help": true, "serve": true, "hook": true}
	for _, v := range manifest.Verbs {
		if nonCallable[v.Name] {
			if v.Callable {
				t.Errorf("Verbs[%q].Callable = true; want false", v.Name)
			}
		} else {
			if !v.Callable {
				t.Errorf("Verbs[%q].Callable = false; want true", v.Name)
			}
		}
	}
}

// ── Phase 3: HandleFree ──────────────────────────────────────────────────────

// TestHandleFreeVerbsIsVersionOnly asserts HandleFreeVerbs() returns exactly
// [version] — the only verb that needs no Client handle.
func TestHandleFreeVerbsIsVersionOnly(t *testing.T) {
	got := manifest.HandleFreeVerbs()
	if len(got) != 1 {
		names := make([]string, len(got))
		for i, v := range got {
			names[i] = v.Name
		}
		t.Fatalf("len(HandleFreeVerbs()) = %d, want 1 (got %v)", len(got), names)
	}
	if got[0].Name != "version" {
		t.Errorf("HandleFreeVerbs()[0].Name = %q, want \"version\"", got[0].Name)
	}
}

// TestVersionHasCallableAndHandleFree asserts that version has both
// Callable: true AND HandleFree: true.
func TestVersionHasCallableAndHandleFree(t *testing.T) {
	v, ok := manifest.Lookup("version")
	if !ok {
		t.Fatal("version not in manifest")
	}
	if !v.Callable {
		t.Errorf("version.Callable = false; want true")
	}
	if !v.HandleFree {
		t.Errorf("version.HandleFree = false; want true")
	}
}

// TestAllOtherVerbsHandleFreeIsFalse asserts every verb except version has
// HandleFree: false.
func TestAllOtherVerbsHandleFreeIsFalse(t *testing.T) {
	for _, v := range manifest.Verbs {
		if v.Name == "version" {
			continue
		}
		if v.HandleFree {
			t.Errorf("Verbs[%q].HandleFree = true; want false (only version is handle-free)", v.Name)
		}
	}
}

// TestHandleFreeVerbsIsNewSlice asserts HandleFreeVerbs() returns a new slice.
func TestHandleFreeVerbsIsNewSlice(t *testing.T) {
	hf := manifest.HandleFreeVerbs()
	if len(hf) == 0 {
		t.Fatal("HandleFreeVerbs() is empty; cannot test slice identity")
	}
	// Verify a copy by mutation: mutating the result must not affect Verbs.
	origName := hf[0].Name
	hf[0].Name = "mutated"
	v, ok := manifest.Lookup("version")
	if !ok {
		t.Fatal("version not in manifest")
	}
	if v.Name != origName {
		t.Errorf("mutating HandleFreeVerbs()[0].Name changed manifest; slices are aliased")
	}
}

// ── Phase 4: Field markers ───────────────────────────────────────────────────

// TestAllowedValuesHaveAtLeastTwoEntries asserts that any field with a non-nil
// AllowedValues slice has at least 2 entries — an enum with one value is a
// constant, not an enum.
func TestAllowedValuesHaveAtLeastTwoEntries(t *testing.T) {
	for _, verb := range manifest.Verbs {
		for _, p := range verb.Params {
			if p.AllowedValues != nil && len(p.AllowedValues) < 2 {
				t.Errorf("Verb %q param %q: AllowedValues has %d entry (want ≥2 or nil)",
					verb.Name, p.Name, len(p.AllowedValues))
			}
		}
		for _, f := range verb.ResultFields {
			if f.AllowedValues != nil && len(f.AllowedValues) < 2 {
				t.Errorf("Verb %q result field %q: AllowedValues has %d entry (want ≥2 or nil)",
					verb.Name, f.Name, len(f.AllowedValues))
			}
		}
	}
}

// TestStateEnumFieldsHaveAllowedValues spot-checks that known enum fields
// have AllowedValues populated.
func TestStateEnumFieldsHaveAllowedValues(t *testing.T) {
	// status.state must have AllowedValues.
	sv, ok := manifest.Lookup("status")
	if !ok {
		t.Fatal("status not in manifest")
	}
	if len(sv.ResultFields) == 0 {
		t.Fatal("status has no ResultFields")
	}
	stateField := sv.ResultFields[0]
	if stateField.Name != "state" {
		t.Fatalf("status.ResultFields[0].Name = %q, want \"state\"", stateField.Name)
	}
	if stateField.AllowedValues == nil {
		t.Errorf("status.state.AllowedValues is nil; want state enum")
	}

	// decide.decision must have AllowedValues.
	dv, ok := manifest.Lookup("decide")
	if !ok {
		t.Fatal("decide not in manifest")
	}
	var decisionParam *manifest.ParamDef
	for i, p := range dv.Params {
		if p.Name == "decision" {
			decisionParam = &dv.Params[i]
			break
		}
	}
	if decisionParam == nil {
		t.Fatal("decide has no decision param")
	}
	if decisionParam.AllowedValues == nil {
		t.Errorf("decide.decision.AllowedValues is nil; want [allow deny]")
	}
}

// TestNullableAndAllowEmptyAreExplicit spot-checks that known nullable fields
// have Nullable: true and known non-nullable fields have Nullable: false.
func TestNullableAndAllowEmptyAreExplicit(t *testing.T) {
	// get.ended_at is Nullable: true (pointer/*time.Time).
	gv, ok := manifest.Lookup("get")
	if !ok {
		t.Fatal("get not in manifest")
	}
	var endedAt *manifest.FieldDef
	for i, f := range gv.ResultFields {
		if f.Name == "ended_at" {
			endedAt = &gv.ResultFields[i]
			break
		}
	}
	if endedAt == nil {
		t.Fatal("get has no ended_at result field")
	}
	if !endedAt.Nullable {
		t.Errorf("get.ended_at.Nullable = false; want true (it is a *time.Time)")
	}

	// spawn.claude_instance_id result field is NOT nullable.
	sv, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	if len(sv.ResultFields) == 0 {
		t.Fatal("spawn has no ResultFields")
	}
	cidField := sv.ResultFields[0]
	if cidField.Nullable {
		t.Errorf("spawn.claude_instance_id.Nullable = true; want false")
	}

	// list.spawns allows empty (non-nil empty slice).
	lv, ok := manifest.Lookup("list")
	if !ok {
		t.Fatal("list not in manifest")
	}
	if len(lv.ResultFields) == 0 {
		t.Fatal("list has no ResultFields")
	}
	spawnsField := lv.ResultFields[0]
	if !spawnsField.AllowEmpty {
		t.Errorf("list.spawns.AllowEmpty = false; want true (empty array is valid)")
	}
}
