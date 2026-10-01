package manifest_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestPauseHasSRDErrorNames pins pause's ErrorNames to exactly SR-1.7's
// seven names, and Client.Pause's Go doc "Errors:" list to the same set.
func TestPauseHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("pause")
	if !ok {
		t.Fatal("pause not in manifest")
	}
	got := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	want := []string{
		"ErrPauseTimeout",
		"ErrSpawnNotFound",
		"ErrSpawnNotPausable",
		"ErrTmuxNotAvailable",
		"ErrTmuxSendKeys",
		"ErrTmuxSessionConflict",
		"ErrTmuxUnresponsive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pause.ErrorNames = %v, want exactly %v", got, want)
	}
	for _, n := range v.ErrorNames {
		if n == "ErrInternal" {
			t.Error(`pause.ErrorNames lists "ErrInternal"`)
		}
	}
	assertGoDocErrorsMatchManifest(t, "Pause", "pause")
}
