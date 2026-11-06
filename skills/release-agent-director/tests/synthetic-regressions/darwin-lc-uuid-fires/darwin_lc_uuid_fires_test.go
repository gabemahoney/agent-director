// Package darwinlcuuidfires_test is the synthetic-regression test for the
// compile phase's darwin LC_UUID gate (b.jh2): releases v0.8.0 to v0.17.7
// shipped darwin binaries without LC_UUID, which macOS 26's dyld refuses.
//
// It writes minimal Mach-O files into a temp RELEASE_DIST_DIR, so it needs
// no darwin build, and runs gates/compile/darwin-lc-uuid.sh against them.
package darwinlcuuidfires_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoRoot walks up from the package working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

const uuidHex = "000102030405060708090a0b0c0d0e0f"

// machO returns a thin arm64 Mach-O executable header with LC_BUILD_VERSION
// (minos 13.0.0) and, when withUUID, LC_UUID.
func machO(withUUID bool) []byte {
	le := binary.LittleEndian
	var cmds []byte
	cmds = le.AppendUint32(cmds, 0x32) // LC_BUILD_VERSION
	cmds = le.AppendUint32(cmds, 24)
	cmds = le.AppendUint32(cmds, 1)      // platform macOS
	cmds = le.AppendUint32(cmds, 13<<16) // minos
	cmds = le.AppendUint32(cmds, 13<<16) // sdk
	cmds = le.AppendUint32(cmds, 0)      // ntools
	ncmds := uint32(1)
	if withUUID {
		cmds = le.AppendUint32(cmds, 0x1b) // LC_UUID
		cmds = le.AppendUint32(cmds, 24)
		for i := byte(0); i < 16; i++ {
			cmds = append(cmds, i)
		}
		ncmds++
	}
	var b []byte
	for _, v := range []uint32{0xfeedfacf, 0x0100000c, 0, 2, ncmds, uint32(len(cmds)), 0, 0} {
		b = le.AppendUint32(b, v)
	}
	return append(b, cmds...)
}

type subCheck struct {
	Name     string `json:"name"`
	Outcome  string `json:"outcome"`
	ExitCode int    `json:"exit_code"`
	Detail   string `json:"detail"`
}

// TestDarwinLCUUIDGate: the gate passes only when both darwin binaries carry
// LC_UUID, and names each failing binary in its own SR-14 diagnostic.
func TestDarwinLCUUIDGate(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	root := repoRoot(t)
	gate := filepath.Join(root, "skills", "release-agent-director", "gates", "compile", "darwin-lc-uuid.sh")
	const main, admin = "agent-director-darwin-arm64", "agent-director-admin-darwin-arm64"

	cases := []struct {
		name      string
		files     map[string][]byte // absent name: no file
		wantExit  int
		wantSubs  []subCheck // Detail names each binary by its file name
		wantDiags []string   // gate names, in order
	}{
		{
			name:     "both_have_lc_uuid",
			files:    map[string][]byte{main: machO(true), admin: machO(true)},
			wantExit: 0,
			wantSubs: []subCheck{
				{"compile.darwin-arm64.lc-uuid", "passed", 0, "PASS " + main + " LC_UUID=" + uuidHex + " minos=13.0.0"},
				{"compile.admin-darwin-arm64.lc-uuid", "passed", 0, "PASS " + admin + " LC_UUID=" + uuidHex + " minos=13.0.0"},
			},
		},
		{
			name:     "admin_lacks_lc_uuid",
			files:    map[string][]byte{main: machO(true), admin: machO(false)},
			wantExit: 1,
			wantSubs: []subCheck{
				{"compile.darwin-arm64.lc-uuid", "passed", 0, "PASS " + main + " LC_UUID=" + uuidHex + " minos=13.0.0"},
				{"compile.admin-darwin-arm64.lc-uuid", "failed", 1, "FAIL " + admin + " LC_UUID=none minos=13.0.0"},
			},
			wantDiags: []string{"compile.admin-darwin-arm64.lc-uuid"},
		},
		{
			name:     "missing_and_not_macho",
			files:    map[string][]byte{admin: []byte("\x7fELF not a Mach-O file")},
			wantExit: 1,
			wantSubs: []subCheck{
				{"compile.darwin-arm64.lc-uuid", "failed", 1, "file not found"},
				{"compile.admin-darwin-arm64.lc-uuid", "failed", 2, "ERROR " + admin + ": invalid magic number in record at byte 0x0"},
			},
			wantDiags: []string{"compile.darwin-arm64.lc-uuid", "compile.admin-darwin-arm64.lc-uuid"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dist := t.TempDir()
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dist, name), data, 0o755); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			cmd := exec.Command("bash", gate, "--worktree-root", root)
			cmd.Env = append(os.Environ(), "RELEASE_DIST_DIR="+dist)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			if got := cmd.ProcessState.ExitCode(); got != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", got, tc.wantExit, stdout.String(), stderr.String())
			}

			var out struct {
				PhaseName string     `json:"phase_name"`
				Outcome   string     `json:"outcome"`
				SubChecks []subCheck `json:"sub_checks"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatalf("parse gate stdout: %v\n%s", err, stdout.String())
			}
			wantOutcome := map[int]string{0: "passed", 1: "failed"}[tc.wantExit]
			if out.PhaseName != "compile" || out.Outcome != wantOutcome {
				t.Errorf("phase_name, outcome = %q, %q; want compile, %q", out.PhaseName, out.Outcome, wantOutcome)
			}
			paths := strings.NewReplacer(main, filepath.Join(dist, main), admin, filepath.Join(dist, admin))
			want := make([]subCheck, len(tc.wantSubs))
			for i, s := range tc.wantSubs {
				s.Detail = paths.Replace(s.Detail)
				want[i] = s
			}
			if !reflect.DeepEqual(out.SubChecks, want) {
				t.Errorf("sub_checks\n got %+v\nwant %+v", out.SubChecks, want)
			}

			var gotDiags []string
			for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
				if line == "" {
					continue
				}
				var d struct {
					Gate     string  `json:"gate"`
					Artifact *string `json:"offending_file_or_artifact"`
				}
				if err := json.Unmarshal([]byte(line), &d); err != nil {
					t.Fatalf("stderr line is not an SR-14 diagnostic: %v\n%s", err, line)
				}
				if d.Artifact == nil || !strings.HasPrefix(*d.Artifact, dist+"/") {
					t.Errorf("diagnostic %s names artifact %v, want a binary under %s", d.Gate, d.Artifact, dist)
				}
				gotDiags = append(gotDiags, d.Gate)
			}
			if !reflect.DeepEqual(gotDiags, tc.wantDiags) {
				t.Errorf("diagnostic gates = %q, want %q", gotDiags, tc.wantDiags)
			}
		})
	}
}
