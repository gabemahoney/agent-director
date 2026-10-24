package main_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDisplayedStateContractCLI: b.zpv regression. The CLI's status, get and
// list keep apitest.CheckDisplayedStateContract: state is the real state,
// an idle_screen row's Displayed State is only in displayed_state, and
// --state matches the real or the displayed state, before --limit.
func TestDisplayedStateContractCLI(t *testing.T) {
	home := t.TempDir()
	if err := apitest.SeedDisplayedStateContract(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
	run := func(args ...string) []byte {
		t.Helper()
		raw, stderr, code := runCLIWithHome(t, home, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("ZPV-FAIL %q: %d %s %s", args, code, raw, stderr)
		}
		return []byte(raw)
	}
	apitest.CheckDisplayedStateContract(t, apitest.DisplayedStateSurface{
		Name: "cli",
		Read: func(verb, id string) []byte { return run(verb, "--instance-id", id) },
		List: func(states []string, limit int) []byte {
			args := []string{"list"}
			if len(states) > 0 {
				args = append(args, "--state", strings.Join(states, ","))
			}
			if limit > 0 {
				args = append(args, "--limit", strconv.Itoa(limit))
			}
			return run(args...)
		},
	})
}
