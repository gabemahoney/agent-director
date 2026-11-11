package realtmux_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// Compare the production serializer with the raw 3.2a witness, including a
// final semicolon, format syntax and a physical newline in every field.
func TestStdinCreateRoundTrip(t *testing.T) {
	rt := newRealTmux(t)
	value := tmuxfix.StdinRoundTripValue
	t.Setenv("OPENROUTER_API_KEY", value)
	cwd := filepath.Join(rt.Dir, value)
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	c := rt.mustCreate(t, createSpec{
		Cwd: cwd, InstanceID: value, ForwardEnv: []string{"OPENROUTER_API_KEY"},
		Envs:     map[string]string{"ROUND_TRIP_EXPLICIT": value},
		UnsetEnv: []string{"ROUND_TRIP_EXPLICIT", "OPENROUTER_API_KEY"},
		Command:  []string{"sh", "-c", "exec sleep 3600", value},
	})
	for name, pair := range map[string][2]string{
		"session environment": {rt.must(t, "show-environment", "-t", c.Reply.SessionID, "OPENROUTER_API_KEY"), "OPENROUTER_API_KEY=" + value + "\n"},
		"cwd":                 {rt.must(t, "display-message", "-p", "-t", c.Reply.PaneID, "#{pane_current_path}"), cwd + "\n"},
		"command":             {rt.must(t, "display-message", "-p", "-t", c.Reply.PaneID, "#{pane_start_command}"), tmuxfix.StdinRoundTripCommand},
		"label":               {rt.must(t, "show-options", "-v", "-t", c.Reply.SessionID, "@ad_owner"), tmuxfix.LabelValue(c.Token, c.Reply.SessionID, value, c.StoreID) + "\n"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s differs from the 3.2a witness: got %q, want %q", name, pair[0], pair[1])
		}
	}
	// Explicit and forwarded values take precedence over the session unset list.
	waitExeced(t, c.Reply.PanePID, "sleep")
	environ := procEnviron(t, c.Reply.PanePID)
	for _, key := range []string{"ROUND_TRIP_EXPLICIT", "OPENROUTER_API_KEY"} {
		if got, present := envValue(environ, key); !present || got != value {
			t.Errorf("pane %s = %q, present %v; want %q despite UnsetEnv", key, got, present, value)
		}
	}
}
