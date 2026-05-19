package manifest_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api/manifest"
)

// TestVerbsContainsExpectedSurface pins the canonical verb order. Each
// Epic that adds a verb appends to this slice; the test catches a missing
// manifest entry on the source-of-truth side (the doc-drift gate catches
// it on the reference-doc side). Order matters: the generator walks Verbs
// in slice order, so a reorder produces a diff in docs/cli-reference.md.
func TestVerbsContainsExpectedSurface(t *testing.T) {
	want := []string{"help", "spawn", "status", "get", "send-keys", "hook"}
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
