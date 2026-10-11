package apitest

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestSeedSpawn_Defaults pins SeedSpawn's empty-argument defaults (id a new
// UUID, state waiting, cwd /tmp, relay_mode off), the optional columns an
// unseeded row reads back as (ExtraEnv an empty non-nil map, WithExtraEnv(nil)
// too; the rest zero; settled at once; agent claude and no end report), and its refusal of a missing store
// unless createStore.
func TestSeedSpawn_Defaults(t *testing.T) {
	t.Parallel()
	if _, err := SeedSpawn(filepath.Join(t.TempDir(), "absent", "state.db"), "x", "", "", "", "", false); err == nil {
		t.Error("SeedSpawn on a missing store with createStore=false: nil error; want a refusal")
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	for _, tc := range []struct {
		name, id, state, cwd, relay string
		opts                        []SpawnOption
	}{
		{"empty id", "", "waiting", "/tmp", "off", nil},
		{"empty state", "def-state", "", "/tmp", "off", nil},
		{"empty cwd", "def-cwd", "waiting", "", "off", nil},
		{"empty relay_mode, nil extra env", "def-relay", "waiting", "/tmp", "", []SpawnOption{WithExtraEnv(nil)}},
	} {
		id, err := SeedSpawn(dbPath, tc.id, tc.state, tc.cwd, tc.relay, "", true, tc.opts...)
		if err != nil {
			t.Fatalf("%s: SeedSpawn: %v", tc.name, err)
		}
		if _, perr := uuid.Parse(id); (tc.id == "" && perr != nil) || (tc.id != "" && id != tc.id) {
			t.Errorf("%s: id = %q; want %q, or a UUID when empty", tc.name, id, tc.id)
		}
		sp := getSpawnV5(t, dbPath, id)
		if sp.State != store.StateWaiting || sp.CWD != "/tmp" || sp.RelayMode != "off" || sp.ExtraEnv == nil ||
			len(sp.ExtraEnv) != 0 || sp.PID != 0 || sp.ProcStarttime != "" || sp.JSONLPath != "" ||
			sp.LivenessUnverifiedSince != "" || sp.LivenessNote != "" {
			t.Errorf("%s: row = %+v; want waiting, /tmp, off, an empty non-nil extra env and the rest unset", tc.name, sp)
		}
		cols, err := ReadSpawnColumns(dbPath, id)
		if err != nil || cols.Agent != store.AgentClaude || cols.EndReportedAt != nil || sp.Agent != store.AgentClaude || sp.EndReportedAtMillis != 0 {
			t.Errorf("%s: agent, end_reported_at = %v, %v (store %q, %d), %v; want claude and no end report", tc.name, cols.Agent, cols.EndReportedAt, sp.Agent, sp.EndReportedAtMillis, err)
		}
		if settled, stamp, err := ReadSettle(dbPath, id); err != nil || settled != store.StateWaiting || stamp != 0 {
			t.Errorf("%s: settle columns = %q, %d, %v; want the row settled (waiting, 0; b.3ie)", tc.name, settled, stamp, err)
		}
	}
}
