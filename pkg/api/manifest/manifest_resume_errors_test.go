package manifest_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestResumeHasSRDErrorNames pins resume's ErrorNames to exactly SR-1.7's
// nine names, and Client.Resume's Go doc "Errors:" list to the same set.
func TestResumeHasSRDErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("resume")
	if !ok {
		t.Fatal("resume not in manifest")
	}
	got := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	want := []string{
		"ErrJsonlMissing",
		"ErrJsonlNeverWritten",
		"ErrNoSessionId",
		"ErrSpawnNotFound",
		"ErrSpawnNotResumable",
		"ErrTmuxNotAvailable",
		"ErrTmuxSessionConflict",
		"ErrTmuxSessionCreate",
		"ErrTmuxUnresponsive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resume.ErrorNames = %v, want exactly %v", got, want)
	}
	for _, n := range v.ErrorNames {
		if n == "ErrInternal" {
			t.Error(`resume.ErrorNames lists "ErrInternal"`)
		}
	}
	assertGoDocErrorsMatchManifest(t, "Resume", "resume")
}
