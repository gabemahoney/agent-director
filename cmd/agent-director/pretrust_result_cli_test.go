package main_test

// The pre_trust result through the built CLI (SR-22.6; AC-SPN-08): spawn
// prints pre_trust in its JSON result, a failed pre-trust exits 0 with one
// "pre-trust failed" stderr line naming the file, and --no-pre-trust prints
// no line, records the opt-out and leaves .claude.json as it was. resume's
// pre-trust is pkg/api's resume_pretrust_test.go and test/envelope-diff's
// resume row; the warning itself is internal/spawn's pretrust_test.go.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreTrustResultCLISpawn: spawn prints pre_trust ok and writes the trust
// entry when .claude.json exists, failed (exit 0, one warning naming the
// file) when it is missing, and skipped with no warning under --no-pre-trust,
// whose choice the row records (no_pre_trust 1).
func TestPreTrustResultCLISpawn(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const seeded = `{"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`
	for _, tc := range []struct {
		name     string
		present  bool // home holds a .claude.json before the launch
		optOut   bool // --no-pre-trust
		want     string
		wantWarn bool
	}{
		{name: "claude.json present", present: true, want: "ok"},
		{name: "claude.json missing", want: "failed", wantWarn: true},
		{name: "no-pre-trust", present: true, optOut: true, want: "skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			path := filepath.Join(home, ".claude.json")
			if tc.present {
				if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
			}
			args := []string{"spawn", "--cwd", cwd}
			if tc.optOut {
				args = append(args, "--no-pre-trust")
			}

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)

			var res map[string]any
			if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil || res["pre_trust"] != tc.want {
				t.Fatalf("exit = %d, stdout = %s; want 0 and pre_trust %q (stderr=%q)", code, stdout, tc.want, stderr)
			}
			var warnings []string
			for _, ln := range strings.Split(stderr, "\n") {
				if strings.Contains(ln, "pre-trust failed") {
					warnings = append(warnings, ln)
				}
			}
			if tc.wantWarn != (len(warnings) == 1 && strings.Contains(warnings[0], path)) || len(warnings) > 1 {
				t.Errorf("stderr pre-trust warnings = %q; want one naming %s: %v", warnings, path, tc.wantWarn)
			}
			got, err := os.ReadFile(path)
			switch {
			case !tc.present && !errors.Is(err, os.ErrNotExist):
				t.Errorf("%s = %q (%v); want still absent", path, got, err)
			case tc.want == "skipped" && string(got) != seeded:
				t.Errorf("%s = %q; want byte-identical to %q", path, got, seeded)
			case tc.want == "ok":
				var top struct {
					Projects map[string]map[string]any `json:"projects"`
				}
				if json.Unmarshal(got, &top) != nil || top.Projects[cwd]["hasTrustDialogAccepted"] != true {
					t.Errorf("%s = %q; want projects[%q].hasTrustDialogAccepted true", path, got, cwd)
				}
			}
			id, _ := res["claude_instance_id"].(string)
			wantNPT := int64(0)
			if tc.optOut {
				wantNPT = 1
			}
			if npt := rowColumns(t, home, id).NoPreTrust; npt != wantNPT {
				t.Errorf("no_pre_trust = %#v; want %d recorded (SR-22.6, SR-5.1)", npt, wantNPT)
			}
		})
	}
}
