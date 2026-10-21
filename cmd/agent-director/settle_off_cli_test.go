package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// settleOffCaptureEnv, set to 1, makes TestSettleOffCLI write the "unset"
// variant's stdout as each case's golden instead of comparing it. The goldens
// pin the output from before b.3ie's settle time, so they are captured once on
// that code and are regenerated only for a deliberate change to get's or list's
// output. So far: E2's agent field (b.ssz), the one byte it changed, and the
// two state-filtered list goldens (list-state-waiting-working and
// list-state-pending-ended-missing), regenerated for the order of their rows
// only: a state-filtered list now also reads the rows that may display as
// stuck (ListFilters.StuckCandidates), a different query whose row order is
// unspecified. The rows and their bytes are unchanged. And b.k6i's
// state_changed_at, added by hand to every get and list row after
// last_seen_at as the fixture's ReadSurfaceStampMs, 2099-01-01T00:00:00.123Z;
// no other byte changed.
const settleOffCaptureEnv = "AGENT_DIRECTOR_CAPTURE_SETTLE_OFF_GOLDENS"

// TestSettleOffCLI: with [state] settle_ms unset or 0, status, get and list
// print exactly their golden over the fixed store (b.3ie: settle time off
// changes no byte); internal/mcp and the TS client compare the same goldens.
func TestSettleOffCLI(t *testing.T) {
	cases := apitest.LoadReadSurfaceCases(t, filepath.Join("testdata", "settle_off"))
	capture := os.Getenv(settleOffCaptureEnv) == "1"
	for _, v := range cases.Variants {
		t.Run(v.Name, func(t *testing.T) {
			home := t.TempDir()
			if _, err := apitest.SeedReadSurfaceStore(stateDB(home)); err != nil {
				t.Fatalf("SeedReadSurfaceStore: %v", err)
			}
			v.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
			for _, c := range cases.Cases {
				stdout, stderr, code := runCLIWithHome(t, home, c.CLIArgs()...)
				if code != 0 || stderr != "" {
					t.Errorf("%s %q: exit %d, stderr %q; want 0 and empty", c.Name, c.CLIArgs(), code, stderr)
					continue
				}
				if capture && v.Name == "unset" {
					if err := os.WriteFile(cases.GoldenPath(c.Name), []byte(stdout), 0o644); err != nil {
						t.Fatalf("capture %s: %v", c.Name, err)
					}
					continue
				}
				if want := cases.Golden(t, c.Name); stdout != want {
					t.Errorf("%s %q: stdout differs from its golden\n got: %s\nwant: %s", c.Name, c.CLIArgs(), stdout, want)
				}
			}
		})
	}
}
