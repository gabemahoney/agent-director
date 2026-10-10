package manifest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// surfaceDoc is the committed surface.json, in generate.go's output shape;
// surfaceVerb, surfaceParam and surfaceField are its per-verb, per-param and
// per-result-field entries.
type surfaceDoc struct {
	Version int           `json:"version"`
	Verbs   []surfaceVerb `json:"verbs"`
}

type surfaceVerb struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Callable     bool           `json:"callable"`
	HandleFree   bool           `json:"handle_free"`
	Params       []surfaceParam `json:"params"`
	ResultFields []surfaceField `json:"result_fields"`
	ErrorNames   []string       `json:"error_names"`
}

type surfaceParam struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Required      bool     `json:"required"`
	Nullable      bool     `json:"nullable"`
	AllowEmpty    bool     `json:"allow_empty"`
	AllowedValues []string `json:"allowed_values"`
}

type surfaceField struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Nullable      bool     `json:"nullable"`
	AllowEmpty    bool     `json:"allow_empty"`
	AllowedValues []string `json:"allowed_values"`
}

// readSurfaceJSON loads the committed surface.json beside this file (located
// via runtime.Caller, so the read is CWD-independent) and returns its raw
// bytes, for byte scans, and the parsed surfaceDoc. The decode is strict: a
// key the surfaceDoc structs do not model, or anything after the top-level
// object, fails the test.
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
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc surfaceDoc
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode surface.json: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("surface.json has data after its top-level object (next token error %v)", err)
	}
	return raw, doc
}

// verbOf returns the manifest's verb name, failing the test when it is absent.
func verbOf(t *testing.T, name string) manifest.VerbDef {
	t.Helper()
	v, ok := manifest.Lookup(name)
	if !ok {
		t.Fatalf("%s not in manifest", name)
	}
	return v
}

// siteText returns verb's description or, when param or field is set, that
// param's or result field's description, failing the test when it is absent.
func siteText(t *testing.T, verb, param, field string) string {
	t.Helper()
	v := verbOf(t, verb)
	switch {
	case param != "":
		for _, p := range v.Params {
			if p.Name == param {
				return p.Description
			}
		}
	case field != "":
		for _, f := range v.ResultFields {
			if f.Name == field {
				return f.Description
			}
		}
	default:
		return v.Description
	}
	t.Fatalf("%s has no param %q / result field %q", verb, param, field)
	return ""
}

// TestSurfaceJSONMirrorsManifest: the committed surface.json carries every
// manifest verb, in order, with its description, markers, params, result
// fields and error names (an absent enum as null, no error names as []), and
// no key beyond those (readSurfaceJSON decodes strictly), so every manifest
// text and marker check in this package holds for surface.json too;
// TestSurfaceJSONUpToDate keeps it regenerated.
func TestSurfaceJSONMirrorsManifest(t *testing.T) {
	_, surface := readSurfaceJSON(t)
	if surface.Version != 1 || len(surface.Verbs) != len(manifest.Verbs) {
		t.Fatalf("surface.json version %d with %d verbs; want version 1 with the manifest's %d", surface.Version, len(surface.Verbs), len(manifest.Verbs))
	}
	for i, v := range manifest.Verbs {
		want := surfaceVerb{Name: v.Name, Description: v.Description, Callable: v.Callable, HandleFree: v.HandleFree,
			Params: []surfaceParam{}, ResultFields: []surfaceField{}, ErrorNames: append([]string{}, v.ErrorNames...)}
		for _, p := range v.Params {
			want.Params = append(want.Params, surfaceParam{p.Name, p.Type, p.Description, p.Required, p.Nullable, p.AllowEmpty, p.AllowedValues})
		}
		for _, f := range v.ResultFields {
			want.ResultFields = append(want.ResultFields, surfaceField{f.Name, f.Type, f.Description, f.Nullable, f.AllowEmpty, f.AllowedValues})
		}
		if !reflect.DeepEqual(surface.Verbs[i], want) {
			t.Errorf("surface.json verb %d:\n got %+v\nwant %+v", i, surface.Verbs[i], want)
		}
	}
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
	want := []string{"help", "spawn", "status", "get", "send-keys", "read-pane", "kill", "decide", "get-permission", "resume", "find-missing", "expire", "make-template", "list", "pause", "serve", "version", "trail-emit", "hook"}
	if got := len(manifest.Verbs); got != len(want) {
		t.Fatalf("len(manifest.Verbs) = %d, want %d (names %v)", got, len(want), want)
	}
	for i, name := range want {
		if manifest.Verbs[i].Name != name {
			t.Errorf("manifest.Verbs[%d].Name = %q, want %q", i, manifest.Verbs[i].Name, name)
		}
	}
}

// TestLookup: Lookup finds a registered verb (help: described, with result
// fields, and a non-nil empty ErrorNames so it marshals as [], not null) and
// returns the zero VerbDef for an unknown one.
func TestLookup(t *testing.T) {
	v, ok := manifest.Lookup("help")
	if !ok || v.Name != "help" || v.Description == "" || len(v.ResultFields) == 0 {
		t.Errorf("Lookup(help) = %+v, %t; want a described help with result fields", v, ok)
	}
	if v.ErrorNames == nil || len(v.ErrorNames) != 0 {
		t.Errorf("help.ErrorNames = %#v; want an empty non-nil slice", v.ErrorNames)
	}
	if v, ok := manifest.Lookup("nonexistent"); ok || !reflect.DeepEqual(v, manifest.VerbDef{}) {
		t.Errorf("Lookup(nonexistent) = %+v, %t; want the zero VerbDef, false", v, ok)
	}
}

// ── Callable and HandleFree ─────────────────────────────────────────────────

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
// order (the 15 callable verbs in the order they appear in Verbs).
func TestCallableVerbsOrder(t *testing.T) {
	want := []string{
		"spawn", "status", "get", "send-keys", "read-pane", "kill",
		"decide", "get-permission", "resume", "find-missing", "expire",
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

// TestVerbSubsetsAreNewSlices: CallableVerbs and HandleFreeVerbs return
// fresh slices, so changing one never changes Verbs.
func TestVerbSubsetsAreNewSlices(t *testing.T) {
	for name, subset := range map[string]func() []manifest.VerbDef{
		"CallableVerbs": manifest.CallableVerbs, "HandleFreeVerbs": manifest.HandleFreeVerbs,
	} {
		got := subset()
		if len(got) == 0 {
			t.Fatalf("%s() is empty; cannot test slice identity", name)
		}
		orig := got[0].Name
		got[0].Name = "mutated"
		if v, ok := manifest.Lookup(orig); !ok || v.Name != orig {
			t.Errorf("mutating %s()[0].Name changed manifest.Verbs; slices are aliased", name)
		}
	}
}

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

// ── Field markers ────────────────────────────────────────────────────────────

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

// TestExtraEnvIsInputOnlyNotOutput is the SR-9.3/SR-10.3 named negative on the
// manifest source of truth: extra_env legitimately exists as an INPUT param
// (spawn + make-template), but MUST NOT appear as a ResultField on ANY verb's
// OUTPUT. The test asserts BOTH poles so it can't be satisfied by simply
// deleting the input param:
//
//   - PRESENT as an input param on spawn and make-template (guards against a
//     regression that would delete the legitimate env-injection surface).
//   - ABSENT from every verb's ResultFields (the output-negative), walked over
//     all verbs, get and list (the row projections most at risk) among them.
func TestExtraEnvIsInputOnlyNotOutput(t *testing.T) {
	// Input-param pole: the env-injection param must exist where it legitimately
	// belongs, under its one manifest name on both verbs (b.c4u; the CLI flag
	// stays --extra-env).
	hasEnvParam := func(verb string) bool {
		v, ok := manifest.Lookup(verb)
		if !ok {
			t.Fatalf("%s not in manifest", verb)
		}
		for _, p := range v.Params {
			if p.Name == "extra_env" {
				return true
			}
		}
		return false
	}
	for _, verb := range []string{"spawn", "make-template"} {
		if !hasEnvParam(verb) {
			t.Errorf("%s is missing the extra_env INPUT param; env-injection surface regressed", verb)
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
			if p.Name == "extra_env" {
				inputParamVerbs[v.Name] = true
			}
		}
		for _, f := range v.ResultFields {
			if f.Name == "extra_env" || f.Name == "extra-env" {
				t.Errorf("surface.json verb %q has an %s result_field; OUTPUT shapes must never carry the input-only env map", v.Name, f.Name)
			}
		}
	}
	// The INPUT param must still be present on spawn + make-template, under its
	// one manifest name (b.c4u).
	for _, verb := range []string{"spawn", "make-template"} {
		if !inputParamVerbs[verb] {
			t.Errorf("surface.json %s is missing the extra_env INPUT param; env-injection surface regressed in the golden", verb)
		}
	}
}

// sorted returns a sorted copy of s, nil for nil.
func sorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return out
}

// shape is a param's or result field's type and markers; enum is compared sorted.
type shape struct {
	typ                  string
	nullable, allowEmpty bool
	enum                 []string
}

// TestFieldShapes pins the type and markers of the params and result fields
// whose shape a requirement names: decide's decision enum; the params whose
// empty value means something (spawn's claude_instance_id mints a fresh id,
// send-keys' text presses Enter only, b.9o4); spawn's one optional bool
// reuse_finished (SR-10.1, AC-REUSE-13); get's nullable ended_at, additive
// liveness fields (SR-8.3) and tmux_socket (SR-3.3, SR-16.1, AC-LKP-22);
// launch_started_at (SR-22.2); pre_trust's value set (SR-22.6, AC-SPN-08,
// AC-CAT-04); kill's kill_sent (SR-6.6) and expire's counts and ids (SR-12.4),
// the only result fields of their verbs, expire with no error names. Each is
// described. status and list carry no tmux_socket (list's spawns text does not
// name it), and make-template takes no reuse param.
func TestFieldShapes(t *testing.T) {
	preTrust := []string{"failed", "ok", "skipped"}
	cases := []struct {
		verb, param, field string
		want               shape
	}{
		{"decide", "decision", "", shape{"string", false, false, []string{"allow", "deny"}}},
		{"spawn", "claude_instance_id", "", shape{"string", false, true, nil}},
		{"send-keys", "text", "", shape{"string", false, true, nil}},
		{"spawn", "reuse_finished", "", shape{"bool", false, false, nil}},
		{"spawn", "", "claude_instance_id", shape{"string", false, false, nil}},
		{"list", "", "spawns", shape{"[]Spawn", false, true, nil}},
		{"get", "", "ended_at", shape{"timestamp?", true, false, nil}},
		{"get", "", "liveness_unverified_since", shape{"timestamp?", true, false, nil}},
		{"get", "", "liveness_note", shape{"string?", true, false, nil}},
		{"get", "", "tmux_socket", shape{"string?", true, false, nil}},
		{"status", "", "launch_started_at", shape{"timestamp?", true, false, nil}},
		{"get", "", "launch_started_at", shape{"timestamp?", true, false, nil}},
		{"spawn", "", "pre_trust", shape{"string", false, false, preTrust}},
		{"resume", "", "pre_trust", shape{"string", false, false, preTrust}},
		{"kill", "", "kill_sent", shape{"bool", false, false, nil}},
		{"expire", "", "count", shape{"int", false, true, nil}},
		{"expire", "", "ids", shape{"[]string", false, true, nil}},
		{"expire", "", "kept", shape{"int", false, true, nil}},
		{"expire", "", "kept_ids", shape{"[]string", false, true, nil}},
	}
	for _, tc := range cases {
		v := verbOf(t, tc.verb)
		var got []shape
		var descs []string
		for _, p := range v.Params {
			if p.Name == tc.param {
				got = append(got, shape{p.Type, p.Nullable, p.AllowEmpty, p.AllowedValues})
				descs = append(descs, p.Description)
				if p.Required && p.Name == "reuse_finished" {
					t.Errorf("%s param %s is required; want optional", tc.verb, p.Name)
				}
			}
		}
		for _, f := range v.ResultFields {
			if f.Name == tc.field {
				got = append(got, shape{f.Type, f.Nullable, f.AllowEmpty, f.AllowedValues})
				descs = append(descs, f.Description)
			}
		}
		if len(got) != 1 || descs[0] == "" {
			t.Errorf("%s %s%s: %d found (descriptions %q); want exactly one, described", tc.verb, tc.param, tc.field, len(got), descs)
			continue
		}
		got[0].enum = sorted(got[0].enum)
		if !reflect.DeepEqual(got[0], tc.want) {
			t.Errorf("%s %s%s shape %+v; want %+v", tc.verb, tc.param, tc.field, got[0], tc.want)
		}
	}
	names := func(verb string) []string {
		var out []string
		for _, f := range verbOf(t, verb).ResultFields {
			out = append(out, f.Name)
		}
		return out
	}
	if got := names("kill"); !reflect.DeepEqual(got, []string{"kill_sent"}) {
		t.Errorf("kill result fields = %v; want only kill_sent", got)
	}
	if got := names("expire"); !reflect.DeepEqual(got, []string{"count", "ids", "kept", "kept_ids"}) {
		t.Errorf("expire result fields = %v; want count, ids, kept, kept_ids", got)
	}
	if got := verbOf(t, "expire").ErrorNames; len(got) != 0 {
		t.Errorf("expire error names = %v; want none", got)
	}
	for _, verb := range []string{"status", "list"} {
		if slices.Contains(names(verb), "tmux_socket") {
			t.Errorf("%s has a tmux_socket result field; want none", verb)
		}
	}
	if text := siteText(t, "list", "", "spawns"); strings.Contains(text, "tmux_socket") {
		t.Errorf("list spawns text names tmux_socket: %q", text)
	}
	for _, p := range verbOf(t, "make-template").Params {
		if p.Name == "reuse-finished" || p.Name == "reuse_finished" {
			t.Errorf("make-template has param %q; want no reuse param", p.Name)
		}
	}
}

// assertErrorNames checks verb's manifest ErrorNames: exactly want when exact,
// else a superset of it; and, method set, that method's Go doc "Errors:"
// list names the same set (ErrInternal is TestNoVerbListsErrInternal's).
func assertErrorNames(t *testing.T, verb, method string, exact bool, want ...string) {
	t.Helper()
	got := sorted(verbOf(t, verb).ErrorNames)
	if exact && !reflect.DeepEqual(got, sorted(want)) {
		t.Errorf("%s.ErrorNames = %v, want exactly %v", verb, got, want)
	}
	for _, n := range want {
		if !slices.Contains(got, n) {
			t.Errorf("%s.ErrorNames missing %q", verb, n)
		}
	}
	if method != "" {
		assertGoDocErrorsMatchManifest(t, method, verb)
	}
}

// TestSpawnHasAllSRDErrorNames: spawn lists every SRD §13.1 validation and
// launch name, among them ErrInvalidFlags (control-character instance id,
// SR-1.7, SR-9.1), ErrTmuxSessionNameInvalid (SR-9.2), ErrTmuxUnresponsive
// (bounded create) and ErrTmuxSessionConflict (label scan, SR-9.3; held name,
// SR-9.4), as Client.Spawn's "Errors:" list does; that bullet names the
// held-name case, and each tmux name reuse returns states its reuse opt-in
// cases (SR-1.4, SR-10).
func TestSpawnHasAllSRDErrorNames(t *testing.T) {
	assertErrorNames(t, "spawn", "Spawn", false, "ErrCwdMissing", "ErrCwdNotAPath", "ErrCwdNotFound", "ErrCwdNotADirectory",
		"ErrRelayModeInvalid", "ErrSpawnDeniedFlag", "ErrReservedEnvKey", "ErrInvalidFlags", "ErrInstanceIdCollision",
		"ErrTmuxSessionNameInvalid", "ErrTmuxNotAvailable", "ErrTmuxSessionCreate", "ErrTmuxUnresponsive", "ErrTmuxSessionConflict")
	conflict := goDocErrorBulletText(t, "Spawn", "ErrTmuxSessionConflict")
	for _, phrase := range []string{`after "duplicate session"`, "the new row is ended"} {
		if !strings.Contains(conflict, phrase) {
			t.Errorf("(*Client).Spawn ErrTmuxSessionConflict bullet lacks %q: %q", phrase, conflict)
		}
	}
	for _, name := range []string{"ErrTmuxSessionConflict", "ErrTmuxUnresponsive", "ErrTmuxNotAvailable", "ErrTmuxSessionCreate"} {
		if text := goDocErrorBulletText(t, "Spawn", name); !strings.Contains(text, "reuse opt-in") {
			t.Errorf("(*Client).Spawn %s bullet does not state its reuse opt-in cases: %q", name, text)
		}
	}
}

// TestListHasSRDErrorNames: list's label k=v parse rejection (SRD §13.1).
func TestListHasSRDErrorNames(t *testing.T) {
	assertErrorNames(t, "list", "", false, "ErrListInvalidLabel")
}

// TestKillHasSRDErrorNames pins kill's ErrorNames, and Client.Kill's "Errors:"
// list, to exactly SR-1.7's five names.
func TestKillHasSRDErrorNames(t *testing.T) {
	assertErrorNames(t, "kill", "Kill", true, "ErrSpawnNotFound", "ErrTmuxKillFailed", "ErrTmuxNotAvailable",
		"ErrTmuxSessionConflict", "ErrTmuxUnresponsive")
}

// TestPauseHasSRDErrorNames pins pause's to exactly SR-1.7's seven names.
func TestPauseHasSRDErrorNames(t *testing.T) {
	assertErrorNames(t, "pause", "Pause", true, "ErrPauseTimeout", "ErrSpawnNotFound", "ErrSpawnNotPausable",
		"ErrTmuxNotAvailable", "ErrTmuxSendKeys", "ErrTmuxSessionConflict", "ErrTmuxUnresponsive")
}

// TestReadPaneHasInteractErrorNames pins read-pane's to exactly SR-1.7's five names.
func TestReadPaneHasInteractErrorNames(t *testing.T) {
	assertErrorNames(t, "read-pane", "ReadPane", true, "ErrSpawnNotFound", "ErrTmuxCaptureFailed", "ErrTmuxNotAvailable",
		"ErrTmuxSessionConflict", "ErrTmuxUnresponsive")
}

// TestResumeHasSRDErrorNames pins resume's to exactly SR-1.7's nine names and
// ErrReservedEnvKey (a row whose stored extra env has HOME, b.nas, or a
// malformed key, b.vpb).
func TestResumeHasSRDErrorNames(t *testing.T) {
	assertErrorNames(t, "resume", "Resume", true, "ErrJsonlMissing", "ErrJsonlNeverWritten", "ErrNoSessionId",
		"ErrReservedEnvKey", "ErrSpawnNotFound", "ErrSpawnNotResumable", "ErrTmuxNotAvailable", "ErrTmuxSessionConflict",
		"ErrTmuxSessionCreate", "ErrTmuxUnresponsive")
}

// TestMakeTemplateHasErrorNames pins make-template's to exactly its three
// template names and ErrReservedEnvKey, spawn's one name for every refused
// extra_env key, reserved or malformed (b.66q).
func TestMakeTemplateHasErrorNames(t *testing.T) {
	assertErrorNames(t, "make-template", "MakeTemplate", true, "ErrTemplateNameUnsafe", "ErrTemplateExists",
		"ErrTemplateMalformed", "ErrReservedEnvKey")
}

// TestSendKeysHasInteractErrorNames pins send-keys' to exactly SR-1.7's seven names.
func TestSendKeysHasInteractErrorNames(t *testing.T) {
	assertErrorNames(t, "send-keys", "SendKeys", true, "ErrSendKeysWhileRelayed", "ErrSpawnNotFound",
		"ErrSpawnNotInteractive", "ErrTmuxNotAvailable", "ErrTmuxSendKeys", "ErrTmuxSessionConflict", "ErrTmuxUnresponsive")
}

// TestNoVerbListsErrInternal pins SR-1.7 / AC-CAT-03 for every verb, callable
// or not: ErrInternal is never a listed error name.
func TestNoVerbListsErrInternal(t *testing.T) {
	for _, v := range manifest.Verbs {
		if slices.Contains(v.ErrorNames, "ErrInternal") {
			t.Errorf("%s.ErrorNames lists ErrInternal; SR-1.7 keeps it off every verb's error list", v.Name)
		}
	}
}
