package main_test

// b.3mx, b.n62: runHook identifies Claude/Codex busy-send events independently
// of unrelated field types; Cursor keeps its strict peek. Reuses the owned-wake pipe fixture.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestHookCLIBusySendTypePolicySelectsOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the hook reads its own identity from /proc")
	}
	fakeDir := buildFakeTmux(t)
	for _, event := range []struct{ name, fixture, stream string }{
		{"FileChanged", "file-changed.json", "stderr"},
		{"PostToolBatch", "post-tool-batch.json", "stdout"},
	} {
		fixture, err := os.ReadFile(filepath.Join("..", "..", "internal", "hook", "testdata", "hook-payloads", event.fixture))
		if err != nil {
			t.Fatal(err)
		}
		for _, agent := range []string{"", "claude", "codex", "cursor"} {
			agentName := agent
			if agentName == "" {
				agentName = "default"
			}
			for _, variant := range []struct{ name, field string }{
				{"string", ""},
				{"file_path_number", "file_path"},
				{"tool_name_number", "tool_name"},
			} {
				t.Run(event.name+"/"+agentName+"/"+variant.name, func(t *testing.T) {
					h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
					rowAgent := agent
					if rowAgent == "" {
						rowAgent = "claude"
					}
					const id = "id-payload-peek"
					h.seedAgentRow(t, id, rowAgent, "off")
					var payload map[string]any
					if err := json.Unmarshal(fixture, &payload); err != nil {
						t.Fatal(err)
					}
					if variant.field != "" {
						payload[variant.field] = 12345
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					r, w, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					defer w.Close()
					fd := w.Fd() // Only the hook may change this initially blocking stream.
					ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
					defer cancel()
					var args []string
					if agent != "" {
						args = []string{"--timeout", "4", "--agent=" + agent}
					}
					cmd := h.hookCmd(ctx, t, id, "", string(raw), args...)
					var other strings.Builder
					if event.stream == "stdout" {
						cmd.Stdout, cmd.Stderr = w, &other
					} else {
						cmd.Stdout, cmd.Stderr = &other, w
					}
					runErr := cmd.Run()
					if ctx.Err() != nil {
						t.Fatalf("hook still running after %s", gateHookDeadline)
					}
					flags, flagsErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					output, readErr := io.ReadAll(r)
					if runErr != nil || other.Len() != 0 || readErr != nil || len(output) != 0 {
						t.Errorf("hook = %v, %s %q, other stream %q (%v); want exit 0 and empty streams", runErr, event.stream, output, other.String(), readErr)
					}
					wantNonblock := variant.field == "" || rowAgent != "cursor"
					if flagsErr != nil || (flags&unix.O_NONBLOCK != 0) != wantNonblock {
						t.Errorf("%s flags = %#x (%v); want O_NONBLOCK=%v", event.stream, flags, flagsErr, wantNonblock)
					}
					assertNoTmuxCall(t, h.home)
				})
			}
		}
	}
}
