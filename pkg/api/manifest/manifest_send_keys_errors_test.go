package manifest_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestSendKeysHasInteractErrorNames pins send-keys' ErrorNames to exactly
// SR-1.7's seven names, and Client.SendKeys' Go doc "Errors:" list to the same set.
func TestSendKeysHasInteractErrorNames(t *testing.T) {
	v, ok := manifest.Lookup("send-keys")
	if !ok {
		t.Fatal("send-keys not in manifest")
	}
	got := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	want := []string{
		"ErrSendKeysWhileRelayed",
		"ErrSpawnNotFound",
		"ErrSpawnNotInteractive",
		"ErrTmuxNotAvailable",
		"ErrTmuxSendKeys",
		"ErrTmuxSessionConflict",
		"ErrTmuxUnresponsive",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("send-keys.ErrorNames = %v, want exactly %v", got, want)
	}
	for _, n := range v.ErrorNames {
		if n == "ErrInternal" {
			t.Error(`send-keys.ErrorNames lists "ErrInternal"`)
		}
	}
	assertGoDocErrorsMatchManifest(t, "SendKeys", "send-keys")
}
