package smoke_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// AssertResultMatchesManifest verifies that result's fields conform to the
// manifest's VerbDef.ResultFields. It uses reflection to match manifest
// snake_case field names against the Go struct's json struct tags.
//
// Field-presence rules:
//   - Nullable=true: field must exist on the struct; value may be zero/nil.
//   - AllowEmpty=true: field must exist; value may be the empty form of its
//     type. Slice/map kinds must be non-nil but may be length-zero; scalar
//     kinds may be the zero value (e.g. empty string, 0, false).
//   - AllowedValues != nil: field must be one of the listed values.
//   - Default (none of the above): field must exist AND be non-zero.
func AssertResultMatchesManifest(t testing.TB, verbDef manifest.VerbDef, result any) {
	t.Helper()
	if len(verbDef.ResultFields) == 0 {
		// Verb returns no fields (e.g. kill, send-keys) — nothing to assert.
		return
	}

	rv := reflect.ValueOf(result)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			t.Errorf("AssertResultMatchesManifest(%s): result is nil pointer", verbDef.Name)
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		t.Errorf("AssertResultMatchesManifest(%s): result is not a struct (got %T)", verbDef.Name, result)
		return
	}
	rt := rv.Type()

	for _, fd := range verbDef.ResultFields {
		// Find the Go struct field whose json tag matches fd.Name.
		sf, fv, found := findFieldByJSONTag(rt, rv, fd.Name)
		if !found {
			t.Errorf("AssertResultMatchesManifest(%s): manifest field %q not found on %T (no matching json tag or Go field name)",
				verbDef.Name, fd.Name, result)
			continue
		}
		_ = sf // available if we need type info

		// AllowedValues: value must be one of the listed strings.
		if len(fd.AllowedValues) > 0 {
			strVal := fmt.Sprintf("%v", fv.Interface())
			if !contains(fd.AllowedValues, strVal) {
				t.Errorf("AssertResultMatchesManifest(%s): field %q = %q not in allowed set %v",
					verbDef.Name, fd.Name, strVal, fd.AllowedValues)
			}
		}

		// Nullable: allowed to be zero/nil — just verify field exists (done above).
		if fd.Nullable {
			continue
		}

		// AllowEmpty: per the manifest's semantics, the field is permitted
		// to be the empty value of its type. For slice/map kinds the empty
		// form must be a non-nil empty collection (pkg/api verbs normalize
		// nil→[] explicitly so callers never see `null` on the wire). For
		// scalar kinds (string, int, bool, pointer) the zero value is
		// acceptable as the "empty form" — strings like `parent_id` /
		// `jsonl_path` / `claude_session_id` are documented as routinely
		// empty when the relevant field has not yet been populated.
		if fd.AllowEmpty {
			k := fv.Kind()
			if k == reflect.Slice || k == reflect.Map {
				if fv.IsNil() {
					t.Errorf("AssertResultMatchesManifest(%s): field %q (AllowEmpty) is nil; expected empty %s not nil",
						verbDef.Name, fd.Name, k)
				}
				continue
			}
			// Scalar field with AllowEmpty=true: zero value is acceptable.
			continue
		}

		// Default: must be non-zero.
		if isZero(fv) {
			t.Errorf("AssertResultMatchesManifest(%s): field %q is zero value; expected non-zero",
				verbDef.Name, fd.Name)
		}
	}
}

// AssertExpectedError verifies that err is one of the error sentinels named by
// verbDef.ErrorNames. Uses errors.Is against the errnames.Catalog. Fails the
// test if err is nil or doesn't match any listed sentinel.
func AssertExpectedError(t testing.TB, verbDef manifest.VerbDef, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("AssertExpectedError(%s): expected an error but got nil", verbDef.Name)
		return
	}
	if len(verbDef.ErrorNames) == 0 {
		// Verb declares no error conditions; any error is unexpected.
		t.Errorf("AssertExpectedError(%s): verb declares no ErrorNames but got error: %v",
			verbDef.Name, err)
		return
	}

	for _, name := range verbDef.ErrorNames {
		sentinel := catalogSentinel(name)
		if sentinel == nil {
			// Name not in catalog — warn but don't treat as a match.
			continue
		}
		if errors.Is(err, sentinel) {
			return // matched
		}
	}

	t.Errorf("AssertExpectedError(%s): error %q does not match any of the verb's ErrorNames %v",
		verbDef.Name, err, verbDef.ErrorNames)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// findFieldByJSONTag looks up a struct field whose json tag primary token
// matches name (snake_case). Falls back to Go field-name matching only when
// no json tag is present at all (safety net for untagged fields).
func findFieldByJSONTag(rt reflect.Type, rv reflect.Value, name string) (reflect.StructField, reflect.Value, bool) {
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		fv := rv.Field(i)

		tag := sf.Tag.Get("json")
		if tag != "" {
			// Primary token is the part before any comma.
			primary := strings.Split(tag, ",")[0]
			if primary == name {
				return sf, fv, true
			}
		} else {
			// No json tag — fall back to exact Go field name (safety net).
			if sf.Name == name {
				return sf, fv, true
			}
		}
	}
	return reflect.StructField{}, reflect.Value{}, false
}

// isZero reports whether v holds the zero value of its type.
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.IsNil() || v.Len() == 0
	default:
		return v.IsZero()
	}
}

// contains reports whether s appears in haystack.
func contains(haystack []string, s string) bool {
	for _, v := range haystack {
		if v == s {
			return true
		}
	}
	return false
}

// catalogSentinel looks up the error sentinel for the given err_name string by
// walking errnames.Catalog. Returns nil when the name is not found.
func catalogSentinel(name string) error {
	for _, entry := range errnames.Catalog {
		if entry.Name == name {
			return entry.Err
		}
	}
	return nil
}
