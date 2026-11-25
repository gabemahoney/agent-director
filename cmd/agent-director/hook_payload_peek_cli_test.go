package main_test

// b.3mx: runHook's parsed --agent must select the peek policy before it
// chooses FileChanged's stderr flags. Reuses the owned-wake CLI pipe fixture.

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

func TestHookCLIFileChangedTypePolicySelectsStderr(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the hook reads its own identity from /proc")
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "internal", "hook", "testdata", "hook-payloads", "file-changed.json"))
	if err != nil {
		t.Fatal(err)
	}
	fakeDir := buildFakeTmux(t)
	for _, agent := range []string{"", "claude", "codex", "cursor"} {
		for _, numeric := range []bool{false, true} {
			name := agent + "/string"
			if numeric {
				name = agent + "/number"
			}
			t.Run(name, func(t *testing.T) {
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
				if numeric {
					payload["file_path"] = 12345
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
				fd := w.Fd() // Like the owned-wake test, start with blocking stderr.
				ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
				defer cancel()
				var args []string
				if agent != "" {
					args = []string{"--timeout", "4", "--agent=" + agent}
				}
				cmd := h.hookCmd(ctx, t, id, "", string(raw), args...)
				var stdout strings.Builder
				cmd.Stdout, cmd.Stderr = &stdout, w
				runErr := cmd.Run()
				if ctx.Err() != nil {
					t.Fatalf("hook still running after %s", gateHookDeadline)
				}
				flags, flagsErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				stderr, readErr := io.ReadAll(r)
				if runErr != nil || stdout.Len() != 0 || readErr != nil || len(stderr) != 0 {
					t.Errorf("hook = %v, stdout %q, stderr %q (%v); want exit 0 and empty streams", runErr, stdout.String(), stderr, readErr)
				}
				wantNonblock := !numeric || rowAgent == "claude"
				if flagsErr != nil || (flags&unix.O_NONBLOCK != 0) != wantNonblock {
					t.Errorf("stderr flags = %#x (%v); want O_NONBLOCK=%v", flags, flagsErr, wantNonblock)
				}
				assertNoTmuxCall(t, h.home)
			})
		}
	}
}
