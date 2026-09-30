package manifest_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestReadPaneHasInteractErrorNames pins read-pane's ErrorNames to exactly
// SR-1.7's five names, and Client.ReadPane's Go doc "Errors:" list to the same set.
func TestReadPaneHasInteractErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("read-pane")
	if !ok {
		t.Fatal("read-pane not in manifest")
	}
	got := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	want := []string{
		"ErrSpawnNotFound",
		"ErrTmuxCaptureFailed",
		"ErrTmuxNotAvailable",
		"ErrTmuxSessionConflict",
		"ErrTmuxUnresponsive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read-pane.ErrorNames = %v, want exactly %v", got, want)
	}
	for _, absent := range []string{"ErrInternal", "ErrSpawnNotInteractive"} {
		for _, n := range v.ErrorNames {
			if n == absent {
				t.Errorf("read-pane.ErrorNames lists %q", absent)
			}
		}
	}
	assertGoDocErrorsMatchManifest(t, "ReadPane", "read-pane")
}
