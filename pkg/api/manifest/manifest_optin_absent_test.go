package manifest_test

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// optInRe matches kill's operator-only finished-row opt-in in any spelling
// (include-finished, include_finished, IncludeFinished, ...).
var optInRe = regexp.MustCompile(`(?i)include.?finished`)

// manifestStrings returns every string manifest.Verbs carries, keyed by its
// path (verb, field, index), walking every struct field, slice and map so a
// text field added later is covered too.
func manifestStrings() map[string]string {
	out := map[string]string{}
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			out[path] = v.String()
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(fmt.Sprintf("%s[%v]", path, k), v.MapIndex(k))
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(path, v.Elem())
			}
		}
	}
	for _, v := range manifest.Verbs {
		walk(v.Name, reflect.ValueOf(v))
	}
	return out
}

// TestKillOptInAbsentFromManifest: no manifest string (verb, parameter or
// result-field name, description, allowed value) names the opt-in (SR-6.8),
// while kill still carries kill_sent and its claude_instance_id parameter.
func TestKillOptInAbsentFromManifest(t *testing.T) {
	strs := manifestStrings()
	if len(strs) == 0 {
		t.Fatal("manifest walk found no strings (precondition)")
	}
	for path, s := range strs {
		if m := optInRe.FindString(s); m != "" {
			t.Errorf("SR-6.8: manifest %s names the operator-only kill opt-in (%q); nothing shown to agents may: %q", path, m, s)
		}
	}

	kill, ok := manifest.Lookup("kill")
	if !ok {
		t.Fatal("kill not in manifest")
	}
	var params []string
	for _, p := range kill.Params {
		params = append(params, p.Name)
	}
	if !reflect.DeepEqual(params, []string{"claude_instance_id"}) {
		t.Errorf("SR-6.8: kill params = %v; want only claude_instance_id (the opt-in is not a manifest parameter)", params)
	}
	if !hasField(kill.ResultFields, "kill_sent") {
		t.Errorf("kill result fields lack kill_sent; the SR-6.8 absence check above would pass vacuously")
	}
}

// TestKillOptInAbsentFromSurfaceJSON is the golden-side twin: the committed
// surface.json names the opt-in nowhere, and kill still carries kill_sent.
func TestKillOptInAbsentFromSurfaceJSON(t *testing.T) {
	raw, surface := readSurfaceJSON(t)
	for _, loc := range optInRe.FindAllIndex(raw, -1) {
		t.Errorf("SR-6.8: surface.json names the operator-only kill opt-in at byte offset %d: %q", loc[0], raw[loc[0]:loc[1]])
	}
	for _, v := range surface.Verbs {
		if v.Name != "kill" {
			continue
		}
		for _, f := range v.ResultFields {
			if f.Name == "kill_sent" {
				return
			}
		}
	}
	t.Error("surface.json kill lacks result field kill_sent; the SR-6.8 absence check above would pass vacuously")
}

// hasField reports whether fields holds one named name.
func hasField(fields []manifest.FieldDef, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}
