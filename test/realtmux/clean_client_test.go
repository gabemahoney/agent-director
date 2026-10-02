package realtmux_test

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// Clean clients (SRD SR-2.2, SR-3.12; Appendix E.11 K2.6; AC-LKP-16 client
// half): a tmux server copies its starting client's environment into its
// global environment, so the production client must start servers from an
// environment holding no AGENT_DIRECTOR_* variable.

// cleanADPrefix is the variable-name prefix the production client removes.
const cleanADPrefix = "AGENT_DIRECTOR_"

// cleanADVars returns the AGENT_DIRECTOR_* entries of environ-style lines as a
// map; a show-environment removal line ("-KEY") maps KEY to "<removed>".
func cleanADVars(lines []string) map[string]string {
	out := map[string]string{}
	for _, line := range lines {
		k, v, _ := strings.Cut(line, "=")
		if strings.HasPrefix(k, "-") {
			k, v = strings.TrimPrefix(k, "-"), "<removed>"
		}
		if strings.HasPrefix(k, cleanADPrefix) {
			out[k] = v
		}
	}
	return out
}

// showADEnv returns the AGENT_DIRECTOR_* entries of a raw show-environment
// with the given scope arguments ("-g", or "-t", <session id>).
func (r *realTmux) showADEnv(t testing.TB, scope ...string) map[string]string {
	t.Helper()
	out := r.must(t, append([]string{"show-environment"}, scope...)...)
	return cleanADVars(strings.Split(strings.TrimSuffix(out, "\n"), "\n"))
}

// TestCleanClientServerEnvironment: a production-created server holds no
// AGENT_DIRECTOR_* variable; a raw client carrying them is the control.
func TestCleanClientServerEnvironment(t *testing.T) {
	tests := []struct {
		name string
		// start starts a server with one session on rt's socket; id is the
		// row's instance id, and caller the test process's AGENT_DIRECTOR_*.
		start func(t *testing.T, rt *realTmux, id string, caller map[string]string) (sessionID string, panePID int)
		// inherits: the server was started from an environment carrying the
		// caller's AGENT_DIRECTOR_* variables and the session has no -e.
		inherits bool
	}{
		{
			name: "production create",
			start: func(t *testing.T, rt *realTmux, id string, _ map[string]string) (string, int) {
				c := rt.mustCreate(t, createSpec{InstanceID: id, Client: newClient()})
				return c.Reply.SessionID, c.Reply.PanePID
			},
		},
		{
			name: "raw client control",
			start: func(t *testing.T, rt *realTmux, _ string, caller map[string]string) (string, int) {
				var kv []string
				for k, v := range caller {
					kv = append(kv, k+"="+v)
				}
				s := rt.raw().withEnv(kv...).startSession(t, "", "")
				return s.ID, s.PanePID
			},
			inherits: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRealTmux(t).fresh(t)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", newInstanceID("caller"))
			t.Setenv("AGENT_DIRECTOR_CLEAN_PROBE", newInstanceID("probe"))
			caller := cleanADVars(os.Environ())
			if _, ok := caller[sandboxguard.EnvVar]; !ok {
				t.Fatalf("the sandbox marker %s is not in the test environment", sandboxguard.EnvVar)
			}
			id := newInstanceID("agent")

			sessionID, panePID := tc.start(t, rt, id, caller)

			own := map[string]string{cleanADPrefix + "INSTANCE_ID": id}
			wantGlobal, wantSession, wantPane := map[string]string{}, own, own
			if tc.inherits {
				wantGlobal, wantSession, wantPane = caller, map[string]string{}, caller
			}
			if got := rt.showADEnv(t, "-g"); !maps.Equal(got, wantGlobal) {
				t.Errorf("global environment AGENT_DIRECTOR_* = %v, want %v", got, wantGlobal)
			}
			if got := rt.showADEnv(t, "-t", sessionID); !maps.Equal(got, wantSession) {
				t.Errorf("session %s environment AGENT_DIRECTOR_* = %v, want %v", sessionID, got, wantSession)
			}
			waitExeced(t, panePID, stubCommand()[0])
			if got := cleanADVars(procEnviron(t, panePID)); !maps.Equal(got, wantPane) {
				t.Errorf("pane process %d environment AGENT_DIRECTOR_* = %v, want %v", panePID, got, wantPane)
			}
		})
	}
}
