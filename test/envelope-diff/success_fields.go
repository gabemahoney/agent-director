// success_fields.go holds the success driver's check of fields a case pins
// (successCase.want) and the kill case's seed (SR-6.6).
package envelope_diff

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fieldsMismatch returns nil when the success envelope carries every field
// of want with its value (as JSON decodes it), and an error naming the first
// that is missing or differs.
func fieldsMismatch(envelope []byte, want map[string]any) error {
	var m map[string]any
	if err := json.Unmarshal(envelope, &m); err != nil {
		return fmt.Errorf("unmarshal envelope: %w", err)
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, ok := m[k]
		if !ok {
			return fmt.Errorf("%s missing; want %v", k, want[k])
		}
		if !reflect.DeepEqual(got, want[k]) {
			return fmt.Errorf("%s = %v (%T); want %v (%T)", k, got, got, want[k], want[k])
		}
	}
	return nil
}

// seedKillGone seeds a waiting row with SeedSpawn's defaults: its socket's
// fake table is empty (the lookup reads Gone) and its recorded pane process
// reads gone, so kill succeeds with no kill sent.
func seedKillGone(t *testing.T) (string, map[string]any) {
	t.Helper()
	const id = "id-kill-1"
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "", "", "", true); err != nil {
		t.Fatalf("seedKillGone: %v", err)
	}
	return filepath.Dir(dbPath), map[string]any{"id": id}
}
