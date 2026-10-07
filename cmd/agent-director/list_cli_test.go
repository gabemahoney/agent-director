package main_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestListCLIFlags: each list flag reaches its ListParams field, --state's
// comma list split and trimmed, and filters AND together; a filter matching
// nothing prints exactly {"spawns":[]} (the filter semantics are pkg/api's
// list_test.go).
func TestListCLIFlags(t *testing.T) {
	home := t.TempDir()
	seed := func(id, state, name, cwd string, opts ...apitest.SpawnOption) {
		t.Helper()
		opts = append(opts, apitest.WithTmuxSessionName(name))
		if _, err := apitest.SeedSpawn(stateDB(home), id, state, cwd, "", "", true, opts...); err != nil {
			t.Fatalf("SeedSpawn %s: %v", id, err)
		}
	}
	seed("a", store.StatePending, "alpha", "/tmp/list-x", apitest.WithRawLabels(`{"role":"worker"}`))
	seed("b", store.StateWaiting, "beta", "/tmp/list-y")
	seed("c", store.StateWorking, "gamma", "/tmp/list-x")
	if err := apitest.SeedParentChild(stateDB(home), "a", "b"); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"a", "b", "c"}},
		{[]string{"--tmux-session-name", "beta"}, []string{"b"}},
		{[]string{"--state", "waiting, working"}, []string{"b", "c"}},
		{[]string{"--tmux-session-name", "beta", "--state", "pending"}, []string{}},
		{[]string{"--label", "role=worker"}, []string{"a"}},
		{[]string{"--parent", "a"}, []string{"b"}},
		{[]string{"--cwd", "/tmp/list-x"}, []string{"a", "c"}},
	} {
		if got := listIDs(t, home, fakeDir, tc.args...); !slices.Equal(got, tc.want) {
			t.Errorf("list %q = %v; want %v", tc.args, got, tc.want)
		}
	}
	if got := listIDs(t, home, fakeDir, "--limit", "1"); len(got) != 1 {
		t.Errorf("list --limit 1 = %v; want one row", got)
	}
	if stdout, stderr, _ := runSpawnCLI(t, home, fakeDir, "list", "--tmux-session-name", "nonexistent"); stdout != "{\"spawns\":[]}\n" {
		t.Errorf("list matching nothing stdout = %q; want {\"spawns\":[]} (stderr=%q)", stdout, stderr)
	}
}
