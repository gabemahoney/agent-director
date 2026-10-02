package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDockerfileNativeCheck runs the measurement image's native-binary check
// (its RUN lines from claude_bin= to the refusal) against a fake npm tree.
func TestDockerfileNativeCheck(t *testing.T) {
	df := readFile(t, "Dockerfile")
	start, end := strings.Index(df, "claude_bin="), strings.Index(df, " && claude --version")
	if start < 0 || end < start {
		t.Fatal("the Dockerfile's native-binary check was not found")
	}
	const elf, big = "\x7fELF", 2 << 20
	for _, tc := range []struct {
		name, head string
		size       int
		elsewhere  bool // claude on PATH resolves to another file of the package
		require    string
		code       int
		launcher   string
	}{
		{"native", elf, big, false, "1", 0, "native\n"},
		{"placeholder launcher", "#!/usr/bin/env node\n", 0, false, "1", 1, "not native: claude resolves to "},
		{"placeholder, not required", "#!/usr/bin/env node\n", 0, false, "0", 0, "not native: claude resolves to "},
		{"small ELF", elf, 4096, false, "1", 1, "not native: "},
		{"claude resolves elsewhere", elf, big, true, "1", 1, "not native: claude resolves to "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			npmRoot, bin, opt := filepath.Join(dir, "npm"), filepath.Join(dir, "bin"), filepath.Join(dir, "opt")
			pkg := filepath.Join(npmRoot, "@anthropic-ai", "claude-code")
			body := tc.head + strings.Repeat("\x00", max(0, tc.size-len(tc.head)))
			target := filepath.Join(pkg, "bin", "claude.exe")
			writeFile(t, target, body)
			if tc.elsewhere {
				target = filepath.Join(pkg, "cli.js")
				writeFile(t, target, body)
			}
			writeFile(t, filepath.Join(bin, "npm"), "#!/bin/sh\n[ \"$1 $2\" = \"root -g\" ] && echo "+npmRoot+"\n")
			for _, exe := range []string{filepath.Join(bin, "npm"), target} {
				if err := os.Chmod(exe, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(bin, "claude")); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(strings.ReplaceAll(df[start:end], "\\\n", ""), "/opt/measure-exit", opt)
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "CLAUDE_CODE_VERSION=2.1.280", "REQUIRE_NATIVE_CLAUDE=" + tc.require}
			out, err := cmd.CombinedOutput()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code || (code != 0) != strings.Contains(string(out), "is not the native binary") {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.code, out)
			}
			if got := readFile(t, filepath.Join(opt, "claude-launcher.txt")); !strings.HasPrefix(got, tc.launcher) {
				t.Errorf("claude-launcher.txt %q, want %q...", got, tc.launcher)
			}
		})
	}
}
