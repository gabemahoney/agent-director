package manifest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// surfaceDoc is the shared decode target for the committed surface.json golden.
// It is the UNION of every field the golden-side tests assert on — verb name,
// param names and descriptions, and the result-field markers (type, nullable,
// description, allowed_values) plus the verb-level description and error
// names — so the golden tests decode through one struct instead
// of each repeating a bespoke anonymous-struct unmarshal. Fields a given test
// does not touch simply stay zero.
type surfaceDoc struct {
	Verbs []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		ErrorNames  []string `json:"error_names"`
		Params      []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"params"`
		ResultFields []struct {
			Name          string   `json:"name"`
			Type          string   `json:"type"`
			Nullable      bool     `json:"nullable"`
			AllowEmpty    bool     `json:"allow_empty"`
			Description   string   `json:"description"`
			AllowedValues []string `json:"allowed_values"`
		} `json:"result_fields"`
	} `json:"verbs"`
}

// readSurfaceJSON loads the committed surface.json sitting beside this test
// file (located via runtime.Caller so the read is CWD-independent) and returns
// both the raw bytes — for tests that byte-scan for a forbidden verb name — and
// the parsed surfaceDoc for tests that assert on structured fields. It
// t.Fatal's on any locate/read/unmarshal failure.
func readSurfaceJSON(t *testing.T) ([]byte, surfaceDoc) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "surface.json"))
	if err != nil {
		t.Fatalf("read surface.json: %v", err)
	}
	var doc surfaceDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal surface.json: %v", err)
	}
	return raw, doc
}

// TestNoMigrationTriggerVerb is the SR-1.6 public-surface guard for the
// CLI/manifest surface: no verb may expose an agent-reachable schema-migration
// trigger. The store migrates only under an out-of-band administrator sentinel
// (see internal/store/migrate_auth.go); there is deliberately NO migrate verb.
//
// The assertion runs against manifest.Verbs — the Go source of truth from which
// surface.json is generated — so it is NOT satisfiable by regenerating the
// golden: adding a migrate verb to manifest.go would flip this test red before
// any `make surface-json` could paper over it. TestNoMigrationTriggerInSurfaceJSON
// covers the committed golden independently.
func TestNoMigrationTriggerVerb(t *testing.T) {
	for _, v := range manifest.Verbs {
		if strings.Contains(strings.ToLower(v.Name), "migrate") {
			t.Errorf("manifest.Verbs contains verb %q whose name implies a migration trigger; "+
				"SR-1.6 forbids any agent-reachable migration verb (migration is admin-sentinel-gated only)", v.Name)
		}
	}
}

// TestNoMigrationTriggerInSurfaceJSON is the golden-side twin of
// TestNoMigrationTriggerVerb: it scans the COMMITTED surface.json bytes for any
// "migrate" verb name. Regenerating the golden from a (hypothetical) migrate
// verb would write "migrate" into these bytes and trip this check, so the
// SR-1.6 invariant survives golden regeneration on the manifest surface.
func TestNoMigrationTriggerInSurfaceJSON(t *testing.T) {
	raw, _ := readSurfaceJSON(t)
	// A verb named "migrate"/"migrate-schema"/etc. serializes as a
	// `"name": "...migrate..."` field. Match the verb-name JSON shape rather
	// than the whole file so the word appearing in a description does not
	// false-positive.
	needles := []string{`"name": "migrate`, `"name":"migrate`}
	body := string(raw)
	for _, n := range needles {
		if strings.Contains(body, n) {
			t.Errorf("surface.json declares a verb name containing %q; SR-1.6 forbids exposing a migration trigger verb", "migrate")
		}
	}
}

// TestVerbsContainsExpectedSurface pins the canonical verb order. Each
// Epic that adds a verb appends to this slice; the test catches a missing
// manifest entry on the source-of-truth side (the doc-drift gate catches
// it on the reference-doc side). Order matters: the generator walks Verbs
// in slice order, so a reorder produces a diff in docs/cli-reference.md.
func TestVerbsContainsExpectedSurface(t *testing.T) {
	want := []string{"help", "spawn", "status", "get", "send-keys", "read-pane", "kill", "decide", "get-permission", "resume", "find-missing", "expire", "delete", "make-template", "list", "pause", "serve", "version", "trail-emit", "hook"}
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
// validation / launch error name from SRD §13.1, including ErrInvalidFlags
// (control-character instance id, SR-1.7, SR-9.1), ErrTmuxSessionNameInvalid
// (reserved session-name characters, SR-9.2), ErrTmuxUnresponsive (bounded
// create) and ErrTmuxSessionConflict (label scan, SR-9.3; held name after
// "duplicate session", SR-9.4), never ErrInternal; Client.Spawn's Go doc
// "Errors:" list must match, and its conflict bullet names the held-name case.
func TestSpawnHasAllSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	want := []string{
		"ErrCwdMissing", "ErrCwdNotAPath", "ErrCwdNotFound", "ErrCwdNotADirectory",
		"ErrRelayModeInvalid", "ErrSpawnDeniedFlag", "ErrReservedEnvKey",
		"ErrInvalidFlags", "ErrInstanceIdCollision", "ErrTmuxSessionNameInvalid",
		"ErrTmuxNotAvailable", "ErrTmuxSessionCreate",
		"ErrTmuxUnresponsive", "ErrTmuxSessionConflict",
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
	if have["ErrInternal"] {
		t.Error(`spawn.ErrorNames lists "ErrInternal"`)
	}
	assertGoDocErrorsMatchManifest(t, "Spawn", "spawn")
	conflict := goDocErrorBulletText(t, "Spawn", "ErrTmuxSessionConflict")
	for _, phrase := range []string{`after "duplicate session"`, "the new row is ended"} {
		if !strings.Contains(conflict, phrase) {
			t.Errorf("(*Client).Spawn ErrTmuxSessionConflict bullet lacks %q: %q", phrase, conflict)
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

// TestKillHasSRDErrorNames pins kill's ErrorNames to exactly SR-1.7's five
// names, and Client.Kill's Go doc "Errors:" list to the same set.
func TestKillHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("kill")
	if !ok {
		t.Fatal("kill not in manifest")
	}
	got := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	want := []string{
		"ErrSpawnNotFound",
		"ErrTmuxKillFailed",
		"ErrTmuxNotAvailable",
		"ErrTmuxSessionConflict",
		"ErrTmuxUnresponsive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kill.ErrorNames = %v, want exactly %v", got, want)
	}
	for _, absent := range []string{"ErrSpawnNotResumable", "ErrInternal"} {
		for _, n := range v.ErrorNames {
			if n == absent {
				t.Errorf("kill.ErrorNames lists %q", absent)
			}
		}
	}
	assertGoDocErrorsMatchManifest(t, "Kill", "kill")
}

// TestResumeIncludesErrTmuxUnresponsive pins that resume advertises the launch
// timeout (SR-1.7) beside its seven earlier names; Epic 16 pins the full list.
func TestResumeIncludesErrTmuxUnresponsive(t *testing.T) {
	v, ok := manifest.Lookup("resume")
	if !ok {
		t.Fatal("resume not in manifest")
	}
	want := []string{
		"ErrSpawnNotFound",
		"ErrSpawnNotResumable",
		"ErrNoSessionId",
		"ErrJsonlMissing",
		"ErrJsonlNeverWritten",
		"ErrTmuxNotAvailable",
		"ErrTmuxSessionCreate",
		"ErrTmuxUnresponsive",
	}
	have := map[string]bool{}
	for _, n := range v.ErrorNames {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("resume.ErrorNames missing %q", n)
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
	nonCallable := map[string]bool{"help": true, "serve": true, "hook": true, "trail-emit": true}
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

// pinnedStateEnum is the byte-identical, order-sensitive spawn state enum
// (SRD §6). SR-8.3's guardrail forbids ANY change to the state enum while
// surfacing the liveness fields — including reorderings and additions.
// TestStateEnumByteIdentity below pins these seven values across all three
// surfaces (manifest source of truth, committed surface.json, and — on the
// TS side, in public-surface.test.ts — that state stays a plain string).
var pinnedStateEnum = []string{
	"pending", "waiting", "working", "ask_user", "check_permission", "ended", "missing",
}

// TestStateEnumByteIdentity is the NAMED negative guard SR-8.3 requires: the
// spawn state enum must be byte-identical (same values, same order) to the
// pinned list on BOTH the manifest source of truth (status + get result
// fields) and the committed surface.json bytes. This is asserted explicitly
// rather than left implied by a golden diff, so a reorder or an added state
// (e.g. slipping the liveness work into a new enum value) trips a red test at
// the source level, not just a golden churn.
func TestStateEnumByteIdentity(t *testing.T) {
	// Manifest source of truth: every result field named "state" that carries
	// an enum must equal the pinned list exactly. status and get both do.
	for _, verbName := range []string{"status", "get"} {
		v, ok := manifest.Lookup(verbName)
		if !ok {
			t.Fatalf("%s not in manifest", verbName)
		}
		var found bool
		for _, f := range v.ResultFields {
			if f.Name != "state" {
				continue
			}
			found = true
			if !reflect.DeepEqual(f.AllowedValues, pinnedStateEnum) {
				t.Errorf("%s.state.AllowedValues = %v; want byte-identical pin %v (SR-8.3: state enum UNTOUCHED)",
					verbName, f.AllowedValues, pinnedStateEnum)
			}
		}
		if !found {
			t.Errorf("%s has no result field named \"state\"; the state enum pin cannot be verified", verbName)
		}
	}

	// Committed surface.json: parse the generated bytes and assert every
	// "state" result field's allowed_values equals the pin. Guards against a
	// regenerated golden silently carrying a mutated enum.
	_, surface := readSurfaceJSON(t)
	stateFieldsSeen := 0
	for _, v := range surface.Verbs {
		for _, f := range v.ResultFields {
			if f.Name != "state" || f.AllowedValues == nil {
				continue
			}
			stateFieldsSeen++
			if !reflect.DeepEqual(f.AllowedValues, pinnedStateEnum) {
				t.Errorf("surface.json verb %q state.allowed_values = %v; want byte-identical pin %v",
					v.Name, f.AllowedValues, pinnedStateEnum)
			}
		}
	}
	if stateFieldsSeen == 0 {
		t.Error("surface.json has no enum-bearing \"state\" result field; the state enum pin cannot be verified")
	}
}

// TestGetLivenessFieldDefsAdditive asserts SR-8.3's literal get surfacing: the
// get verb gains exactly the two additive nullable liveness FieldDefs, with
// the nullable "?" types and Nullable=true, and NEITHER carries an enum
// (AllowedValues must stay nil). This is the source-of-truth twin of the
// surface.json content assertion below.
func TestGetLivenessFieldDefsAdditive(t *testing.T) {
	v, ok := manifest.Lookup("get")
	if !ok {
		t.Fatal("get not in manifest")
	}
	byName := map[string]manifest.FieldDef{}
	for _, f := range v.ResultFields {
		byName[f.Name] = f
	}

	cases := []struct {
		name    string
		wantTyp string
	}{
		{"liveness_unverified_since", "timestamp?"},
		{"liveness_note", "string?"},
	}
	for _, c := range cases {
		f, ok := byName[c.name]
		if !ok {
			t.Errorf("get result field %q missing; SR-8.3 requires the additive get FieldDef", c.name)
			continue
		}
		if f.Type != c.wantTyp {
			t.Errorf("get.%s.Type = %q; want %q (nullable marker)", c.name, f.Type, c.wantTyp)
		}
		if !f.Nullable {
			t.Errorf("get.%s.Nullable = false; want true", c.name)
		}
		if f.AllowedValues != nil {
			t.Errorf("get.%s.AllowedValues = %v; want nil (not an enum)", c.name, f.AllowedValues)
		}
	}
}

// TestGetLivenessFieldsInSurfaceJSON is the committed-golden twin of
// TestGetLivenessFieldDefsAdditive: the two additive get FieldDefs must be
// present in surface.json (as timestamp?/string? nullable fields), so a
// regenerated golden that dropped them trips this named check rather than
// only showing up as a silent diff.
func TestGetLivenessFieldsInSurfaceJSON(t *testing.T) {
	_, surface := readSurfaceJSON(t)

	var getFields map[string]struct {
		typ      string
		nullable bool
	}
	for _, v := range surface.Verbs {
		if v.Name != "get" {
			continue
		}
		getFields = map[string]struct {
			typ      string
			nullable bool
		}{}
		for _, f := range v.ResultFields {
			getFields[f.Name] = struct {
				typ      string
				nullable bool
			}{f.Type, f.Nullable}
		}
	}
	if getFields == nil {
		t.Fatal("surface.json has no get verb")
	}
	for name, wantTyp := range map[string]string{
		"liveness_unverified_since": "timestamp?",
		"liveness_note":             "string?",
	} {
		f, ok := getFields[name]
		if !ok {
			t.Errorf("surface.json get verb missing result field %q (additive FieldDef must be generated)", name)
			continue
		}
		if f.typ != wantTyp {
			t.Errorf("surface.json get.%s.type = %q; want %q", name, f.typ, wantTyp)
		}
		if !f.nullable {
			t.Errorf("surface.json get.%s.nullable = false; want true", name)
		}
	}
}

// TestExtraEnvIsInputOnlyNotOutput is the SR-9.3/SR-10.3 named negative on the
// manifest source of truth: extra_env legitimately exists as an INPUT param
// (spawn + make-template), but MUST NOT appear as a ResultField on ANY verb's
// OUTPUT. The test asserts BOTH poles so it can't be satisfied by simply
// deleting the input param:
//
//   - PRESENT as an input param on spawn and make-template (guards against a
//     regression that would delete the legitimate env-injection surface).
//   - ABSENT from every verb's ResultFields (the output-negative) — walked over
//     all verbs, with get and list called out by name since they carry the row
//     projections most at risk of accidentally gaining the column.
func TestExtraEnvIsInputOnlyNotOutput(t *testing.T) {
	// Input-param pole: the env-injection param must exist where it legitimately
	// belongs. The verb-param spelling differs (spawn's CLI flag is "extra-env",
	// make-template's json key is "extra_env"); accept either kebab/snake form so
	// the guard tracks the param regardless of the surface's flag convention.
	hasEnvParam := func(verb string) bool {
		v, ok := manifest.Lookup(verb)
		if !ok {
			t.Fatalf("%s not in manifest", verb)
		}
		for _, p := range v.Params {
			if p.Name == "extra_env" || p.Name == "extra-env" {
				return true
			}
		}
		return false
	}
	for _, verb := range []string{"spawn", "make-template"} {
		if !hasEnvParam(verb) {
			t.Errorf("%s is missing the extra-env/extra_env INPUT param; env-injection surface regressed", verb)
		}
	}

	// Output-negative pole: no verb's ResultFields may carry extra_env (either
	// spelling).
	for _, v := range manifest.Verbs {
		for _, f := range v.ResultFields {
			if f.Name == "extra_env" || f.Name == "extra-env" {
				t.Errorf("verb %q has an %s OUTPUT ResultField; extra_env is INPUT-only and must never surface on a result row", v.Name, f.Name)
			}
		}
	}

	// Explicit named checks on the two row-projection verbs most at risk.
	for _, verb := range []string{"get", "list"} {
		v, ok := manifest.Lookup(verb)
		if !ok {
			t.Fatalf("%s not in manifest", verb)
		}
		for _, f := range v.ResultFields {
			if f.Name == "extra_env" || f.Name == "extra-env" {
				t.Errorf("%s.ResultFields carries %s; the OUTPUT row must not expose the input-only env map", verb, f.Name)
			}
		}
	}
}

// TestExtraEnvAbsentFromOutputSurfaceJSON is the committed-golden twin: extra_env
// must NOT appear in any verb's result_fields in surface.json (the OUTPUT shape),
// while it MUST remain present as a spawn/make-template param (the INPUT shape).
// A regenerated golden that leaked extra_env onto an output row trips this named
// check rather than passing silently as a self-consistent regeneration.
func TestExtraEnvAbsentFromOutputSurfaceJSON(t *testing.T) {
	_, surface := readSurfaceJSON(t)

	inputParamVerbs := map[string]bool{}
	for _, v := range surface.Verbs {
		for _, p := range v.Params {
			if p.Name == "extra_env" || p.Name == "extra-env" {
				inputParamVerbs[v.Name] = true
			}
		}
		for _, f := range v.ResultFields {
			if f.Name == "extra_env" || f.Name == "extra-env" {
				t.Errorf("surface.json verb %q has an %s result_field; OUTPUT shapes must never carry the input-only env map", v.Name, f.Name)
			}
		}
	}
	// The INPUT param must still be present on spawn + make-template (either
	// kebab/snake spelling).
	for _, verb := range []string{"spawn", "make-template"} {
		if !inputParamVerbs[verb] {
			t.Errorf("surface.json %s is missing the extra-env/extra_env INPUT param; env-injection surface regressed in the golden", verb)
		}
	}
}

// TestListSpawnsDescriptionNamesLivenessFields pins the list surfacing path:
// per the PM-ratified interpretation, list gains the liveness fields via an
// extended composite `spawns` Description (the list manifest declares one
// composite []Spawn field, never per-column FieldDefs). The Description must
// name both fields on the source of truth AND in the committed surface.json.
func TestListSpawnsDescriptionNamesLivenessFields(t *testing.T) {
	v, ok := manifest.Lookup("list")
	if !ok {
		t.Fatal("list not in manifest")
	}
	if len(v.ResultFields) == 0 || v.ResultFields[0].Name != "spawns" {
		t.Fatalf("list.ResultFields[0] is not the composite \"spawns\" field; got %+v", v.ResultFields)
	}
	desc := v.ResultFields[0].Description
	for _, needle := range []string{"liveness_unverified_since", "liveness_note"} {
		if !strings.Contains(desc, needle) {
			t.Errorf("list.spawns Description does not name %q; SR-8.3 surfaces list liveness via the composite description; got %q",
				needle, desc)
		}
	}

	// Committed surface.json twin.
	_, surface := readSurfaceJSON(t)
	var sjDesc string
	for _, vv := range surface.Verbs {
		if vv.Name != "list" {
			continue
		}
		for _, f := range vv.ResultFields {
			if f.Name == "spawns" {
				sjDesc = f.Description
			}
		}
	}
	if sjDesc == "" {
		t.Fatal("surface.json list verb has no spawns result field description")
	}
	for _, needle := range []string{"liveness_unverified_since", "liveness_note"} {
		if !strings.Contains(sjDesc, needle) {
			t.Errorf("surface.json list.spawns description does not name %q; got %q", needle, sjDesc)
		}
	}
}

// TestSpawnParamDescriptionsPinValidationRules pins the load-bearing tokens of
// three spawn param descriptions, on the manifest source of truth AND in the
// committed surface.json: the tmux-session-name text must name '$' and '\'
// among the rejected characters (SR-9.2), the claude_instance_id text must
// state that an id with a control character is rejected with ErrInvalidFlags
// (SR-18.9), and the no-pre-trust text must state that the choice is recorded
// for the row's life and followed by every resume of it (SR-22.6).
// Only tokens are asserted, never full sentences, so wording edits
// do not break the test. The claude_instance_id collision sentence is
// deliberately not pinned.
func TestSpawnParamDescriptionsPinValidationRules(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	surfaceDesc := map[string]string{}
	for _, vv := range surface.Verbs {
		if vv.Name != "spawn" {
			continue
		}
		for _, p := range vv.Params {
			surfaceDesc[p.Name] = p.Description
		}
	}
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	manifestDesc := map[string]string{}
	for _, p := range v.Params {
		manifestDesc[p.Name] = p.Description
	}

	cases := []struct {
		name   string
		param  string
		tokens []string
	}{
		{"session name rejects dollar and backslash", "tmux-session-name", []string{`'$'`, `'\'`}},
		{"instance id rejects control characters", "claude_instance_id", []string{"control character", "ErrInvalidFlags"}},
		{"no-pre-trust is recorded for the life", "no-pre-trust", []string{"recorded on the row for its life", "every resume of that life follows it"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for source, descs := range map[string]map[string]string{"manifest": manifestDesc, "surface.json": surfaceDesc} {
				desc, ok := descs[tc.param]
				if !ok {
					t.Fatalf("%s: spawn has no %q param", source, tc.param)
				}
				for _, tok := range tc.tokens {
					if !strings.Contains(desc, tok) {
						t.Errorf("%s: spawn %s description does not contain %q; got %q", source, tc.param, tok, desc)
					}
				}
			}
		})
	}
}

// TestNoVerbListsErrInternal pins SR-1.7 / AC-CAT-03 for every verb, callable
// or not: ErrInternal is never a listed error name, on the manifest source of
// truth or in the committed surface.json.
func TestNoVerbListsErrInternal(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	if len(surface.Verbs) == 0 {
		t.Fatal("surface.json declares no verbs")
	}
	for _, v := range manifest.Verbs {
		for _, n := range v.ErrorNames {
			if n == "ErrInternal" {
				t.Errorf("manifest: %s.ErrorNames lists ErrInternal; SR-1.7 keeps it off every verb's error list", v.Name)
			}
		}
	}
	for _, v := range surface.Verbs {
		for _, n := range v.ErrorNames {
			if n == "ErrInternal" {
				t.Errorf("surface.json: %s error_names lists ErrInternal; SR-1.7 keeps it off every verb's error list", v.Name)
			}
		}
	}
}

// TestSpawnDescriptionStatesPreCheckErrInternal pins AC-CAT-03's second half:
// spawn's Description names the ErrInternal trigger (the collision pre-check's
// failed store read). Tokens only, on the manifest and in surface.json.
func TestSpawnDescriptionStatesPreCheckErrInternal(t *testing.T) {
	v, ok := manifest.Lookup("spawn")
	if !ok {
		t.Fatal("spawn not in manifest")
	}
	descs := map[string]string{"manifest": v.Description}
	_, surface := readSurfaceJSON(t)
	for _, vv := range surface.Verbs {
		if vv.Name == "spawn" {
			descs["surface.json"] = vv.Description
		}
	}
	if _, ok := descs["surface.json"]; !ok {
		t.Fatal("surface.json has no spawn verb")
	}
	for source, desc := range descs {
		for _, tok := range []string{"ErrInternal", "collision pre-check", "read the store"} {
			if !strings.Contains(desc, tok) {
				t.Errorf("%s: spawn description does not contain %q; got %q", source, tok, desc)
			}
		}
	}
}

// TestResumeDescriptionStatesPreTrust pins SR-22.6 on resume's Description:
// resume pre-trusts unless the row's spawn opted out with no-pre-trust, and a
// pre-trust failure never fails the resume. Tokens only, on the manifest and in
// surface.json (help shows this same description).
func TestResumeDescriptionStatesPreTrust(t *testing.T) {
	v, ok := manifest.Lookup("resume")
	if !ok {
		t.Fatal("resume not in manifest")
	}
	descs := map[string]string{"manifest": v.Description}
	_, surface := readSurfaceJSON(t)
	for _, vv := range surface.Verbs {
		if vv.Name == "resume" {
			descs["surface.json"] = vv.Description
		}
	}
	if _, ok := descs["surface.json"]; !ok {
		t.Fatal("surface.json has no resume verb")
	}
	for source, desc := range descs {
		for _, tok := range []string{"best-effort pre-trust", "unless the spawn that began the row's life",
			"no-pre-trust", "pre-trust failure never fails the resume"} {
			if !strings.Contains(desc, tok) {
				t.Errorf("%s: resume description does not contain %q; got %q", source, tok, desc)
			}
		}
	}
}

// resultField is one verb's result field as a source (manifest or
// surface.json) gives it.
type resultField struct {
	typ        string
	nullable   bool
	allowEmpty bool
	desc       string
	enum       []string
}

// resultFieldSources returns verb's result field named field keyed by source
// ("manifest", "surface.json"); a source lacking the field has no key.
func resultFieldSources(t *testing.T, surface surfaceDoc, verb, field string) map[string]resultField {
	t.Helper()
	sources := map[string]resultField{}
	v, ok := manifest.Lookup(verb)
	if !ok {
		t.Fatalf("%s not in manifest", verb)
	}
	for _, f := range v.ResultFields {
		if f.Name == field {
			sources["manifest"] = resultField{f.Type, f.Nullable, f.AllowEmpty, f.Description, f.AllowedValues}
		}
	}
	for _, sv := range surface.Verbs {
		if sv.Name != verb {
			continue
		}
		for _, f := range sv.ResultFields {
			if f.Name == field {
				sources["surface.json"] = resultField{f.Type, f.Nullable, f.AllowEmpty, f.Description, f.AllowedValues}
			}
		}
	}
	return sources
}

// TestLaunchStartedAtResultFields pins SR-22.2 on the manifest and in
// surface.json: status and get carry a nullable timestamp? launch_started_at
// field, list names it in its spawns text, each stating the SR-22.2 rule.
func TestLaunchStartedAtResultFields(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	cases := []struct {
		verb    string
		field   string // the result field holding the text
		listRow bool
	}{
		{"status", "launch_started_at", false},
		{"get", "launch_started_at", false},
		{"list", "spawns", true},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			sources := resultFieldSources(t, surface, tc.verb, tc.field)
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: %s has no %q result field", source, tc.verb, tc.field)
					continue
				}
				if !tc.listRow {
					if f.allowEmpty {
						t.Errorf("%s: %s.%s allow_empty = true; want false", source, tc.verb, tc.field)
					}
					if f.typ != "timestamp?" || !f.nullable {
						t.Errorf("%s: %s.launch_started_at type %q nullable %v; want \"timestamp?\" nullable true",
							source, tc.verb, f.typ, f.nullable)
					}
					if f.enum != nil {
						t.Errorf("%s: %s.launch_started_at allowed values %v; want none", source, tc.verb, f.enum)
					}
				}
				apitest.AssertAgentTextCase(t, source+": "+tc.verb+" result field "+tc.field, f.desc,
					apitest.DescLaunchStartedAtField(tc.listRow))
			}
		})
	}
}

// TestPreTrustResultField pins SR-22.6's pre_trust result field (AC-SPN-08,
// AC-CAT-04) on spawn and resume, on the manifest and in surface.json: a
// non-nullable, non-empty string whose value set is exactly ok/skipped/failed,
// its text stating each meaning, and the verb description (all help shows)
// naming it. Key phrases only, never full sentences.
func TestPreTrustResultField(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	cases := []struct {
		verb       string
		skippedWhy string // why pre-trust was off for this launch
	}{
		{"spawn", "the caller passed no-pre-trust"},
		{"resume", "the spawn that began the row's life turned it off with no-pre-trust"},
	}
	wantEnum := []string{"failed", "ok", "skipped"}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			sources := resultFieldSources(t, surface, tc.verb, "pre_trust")
			for _, source := range []string{"manifest", "surface.json"} {
				f, ok := sources[source]
				if !ok {
					t.Errorf("%s: %s has no pre_trust result field", source, tc.verb)
					continue
				}
				if f.typ != "string" || f.nullable || f.allowEmpty {
					t.Errorf("%s: %s.pre_trust type %q nullable %v allow_empty %v; want \"string\" false false",
						source, tc.verb, f.typ, f.nullable, f.allowEmpty)
				}
				got := append([]string(nil), f.enum...)
				sort.Strings(got)
				if !reflect.DeepEqual(got, wantEnum) {
					t.Errorf("%s: %s.pre_trust allowed values %v; want exactly ok, skipped, failed", source, tc.verb, f.enum)
				}
				for _, tok := range []string{
					"ok = the folder-trust entry was written",
					"skipped = pre-trust was off for this launch", tc.skippedWhy, "nothing was attempted",
					"failed = pre-trust was attempted", "entry was not written", "launch still proceeds",
				} {
					if !strings.Contains(f.desc, tok) {
						t.Errorf("%s: %s.pre_trust description does not contain %q; got %q", source, tc.verb, tok, f.desc)
					}
				}
			}

			v, _ := manifest.Lookup(tc.verb)
			descs := map[string]string{"manifest": v.Description}
			for _, sv := range surface.Verbs {
				if sv.Name == tc.verb {
					descs["surface.json"] = sv.Description
				}
			}
			for _, source := range []string{"manifest", "surface.json"} {
				for _, tok := range []string{"Returns the claude_instance_id and pre_trust", "ok, skipped or failed"} {
					if !strings.Contains(descs[source], tok) {
						t.Errorf("%s: %s description does not contain %q; got %q", source, tc.verb, tok, descs[source])
					}
				}
			}
		})
	}
}
