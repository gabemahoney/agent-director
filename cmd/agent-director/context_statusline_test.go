package main_test

import (
	"encoding/base64"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"strings"
	"testing"
)

func TestContextStatuslineForwardsOriginalCommand(t *testing.T) {
	for _, name := range []string{"captured", "oversized stdin", "malformed stdin"} {
		t.Run(name, func(t *testing.T) {
			raw := string(contextfix.Must(t, "claude-parent").Callback)
			if name == "oversized stdin" {
				raw = strings.Repeat("x\x00\n", int(hook.MaxPayloadBytes)) + "final bytes\n"
			}
			if name == "malformed stdin" {
				raw = "{partial"
			}
			home := t.TempDir()
			original := "cat\nprintf '%s' 'stderr \"quotes\" \\ $()' >&2\nexit 17"
			// The command's native stdin/stdout/stderr/status must survive AD admission failure.
			out, errOut, code := mustRun(t, cliOpts{env: homeEnv(home, nil), stdin: raw, deadline: surfaceDeadline}, "hook", "--context-source", "statusLine", "--context-forward", base64.RawStdEncoding.EncodeToString([]byte(original)))
			if out != raw || errOut != "stderr \"quotes\" \\ $()" || code != 17 {
				t.Errorf("forwarding: stdout preserved=%v, stderr=%q, exit=%d", out == raw, errOut, code)
			}
		})
	}
}
