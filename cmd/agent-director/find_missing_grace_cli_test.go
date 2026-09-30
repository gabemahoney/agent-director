package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The CLI binary takes no injected clock, so every launch start below sits at
// least 10 s from any grace boundary against the real current time (the exact
// 59/61 s boundaries are covered in pkg/api).

// graceHome bootstraps a throwaway HOME, writes its [tmux] settings through the
// config writer and seeds one pending row with the given launch start option.
func graceHome(t *testing.T, id string, launch apitest.SpawnOption, settings ...apitest.TmuxSetting) (home, dbPath string) {
	t.Helper()
	home = t.TempDir()
	bootstrapDB(t, home)
	apitest.WriteTmuxConfig(t, filepath.Join(directorDir(home), "config.toml"), settings...)
	dbPath = stateDB(home)
	if _, err := apitest.SeedSpawn(dbPath, id, "pending", "/tmp", "off", "", false, launch); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return home, dbPath
}

// assertFindMissingGrace runs find-missing and checks whether id was judged
// (marked missing and in ids) or left pending and in neither result list.
func assertFindMissingGrace(t *testing.T, home, dbPath, id string, wantMarked bool) {
	t.Helper()
	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}
	res := parseFindMissingResult(t, stdout)
	if slices.Contains(res.UnverifiedIDs, id) {
		t.Errorf("unverified_ids = %v; want %s absent", res.UnverifiedIDs, id)
	}
	state := readSpawnState(t, dbPath, id)
	if wantMarked {
		if !slices.Equal(res.IDs, []string{id}) || state != "missing" {
			t.Errorf("ids = %v, state = %q; want [%s], missing (judged past grace)", res.IDs, state, id)
		}
		return
	}
	if len(res.IDs) != 0 || state != "pending" {
		t.Errorf("ids = %v, state = %q; want [], pending (untouched inside grace)", res.IDs, state)
	}
}

// graceID is a per-process-unique instance id, so no live sandbox process
// holds it and the environ fallback judges a past-grace row dead.
func graceID(name string) string {
	return fmt.Sprintf("id-fm-grace-%d-%s", os.Getpid(), name)
}

// TestFindMissingCLIGraceDefault: under the default setting a fresh pending row
// is untouched; one several minutes old or with no launch start is marked.
func TestFindMissingCLIGraceDefault(t *testing.T) {
	cases := []struct {
		name   string
		launch func(now time.Time) apitest.SpawnOption
		marked bool
	}{
		{"fresh launch start", func(now time.Time) apitest.SpawnOption {
			return apitest.WithLaunchStartedAt(now.UnixMilli())
		}, false},
		{"launch start minutes old", func(now time.Time) apitest.SpawnOption {
			return apitest.WithLaunchStartedAt(now.Add(-5 * time.Minute).UnixMilli())
		}, true},
		{"no launch start", func(time.Time) apitest.SpawnOption {
			return apitest.WithNoLaunchStartedAt()
		}, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := graceID(fmt.Sprintf("default-%d", i))
			home, dbPath := graceHome(t, id, tc.launch(time.Now()))
			assertFindMissingGrace(t, home, dbPath, id, tc.marked)
		})
	}
}

// TestFindMissingCLIGraceConfigured: a row aged between the safe minimum and the
// default stays pending, then is marked once the key is the minimum (AC-CFG-02).
func TestFindMissingCLIGraceConfigured(t *testing.T) {
	minimum := config.PendingGraceMinimumSeconds(config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs)
	if config.DefaultPendingGraceSeconds-minimum < 20 {
		t.Fatalf("default %d s - minimum %d s < 20 s: no 10 s margin either side", config.DefaultPendingGraceSeconds, minimum)
	}
	age := time.Duration(minimum+config.DefaultPendingGraceSeconds) * time.Second / 2

	id := graceID("configured")
	home, dbPath := graceHome(t, id, apitest.WithLaunchStartedAt(time.Now().Add(-age).UnixMilli()))
	assertFindMissingGrace(t, home, dbPath, id, false)

	apitest.WriteTmuxConfig(t, filepath.Join(directorDir(home), "config.toml"),
		apitest.TmuxInt(config.TmuxPendingGraceSeconds, minimum))
	assertFindMissingGrace(t, home, dbPath, id, true)
}
