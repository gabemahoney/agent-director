package main_test

// pretrust_result_cli_test.go covers the pre_trust result field on the CLI
// (SR-22.6 "The field", "Surfaces" and "Standard error"; AC-SPN-08): spawn
// and resume print pre_trust (ok, skipped or failed) in their JSON result, a
// failed pre-trust exits 0 with one "pre-trust failed" stderr line naming the
// file, and an opted-out launch prints no line and leaves .claude.json as it
// was. Every case runs under its own HOME and TMUX_TMPDIR, so the real
// ~/.claude.json and ~/.agent-director are never touched.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ptrSeedJSON is the compact .claude.json a present-file case starts with,
// trusting another folder only, so any rewrite changes its bytes.
const ptrSeedJSON = `{"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`

// ptrCase is one pre-trust result case: whether home holds a .claude.json,
// whether pre-trust is off for the launch, and the outcome the result reports.
type ptrCase struct {
	name     string
	present  bool   // home holds a .claude.json before the launch
	optOut   bool   // spawn: --no-pre-trust; resume: the row records the opt-out
	want     string // the pre_trust the result reports
	wantNPT  int64  // spawn only: the no_pre_trust the insert records
	wantWarn bool   // stderr holds one "pre-trust failed" line naming the file
}

// ptrClaudeJSON is home's .claude.json and its bytes as seeded (nil when the
// case starts without the file).
type ptrClaudeJSON struct {
	path   string
	before []byte
}

// ptrSeedClaudeJSON writes home's .claude.json when present is true.
func ptrSeedClaudeJSON(t *testing.T, home string, present bool) ptrClaudeJSON {
	t.Helper()
	c := ptrClaudeJSON{path: filepath.Join(home, ".claude.json")}
	if !present {
		return c
	}
	c.before = []byte(ptrSeedJSON)
	if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
		t.Fatalf("seed %s: %v", c.path, err)
	}
	return c
}

// check asserts the file trusts cwd when want is ok, and otherwise is exactly
// as seeded (still absent when it was).
func (c ptrClaudeJSON) check(t *testing.T, cwd, want string) {
	t.Helper()
	got, err := os.ReadFile(c.path)
	if want != "ok" {
		if c.before == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s = %q (%v); want still absent", c.path, got, err)
			}
		} else if err != nil || string(got) != string(c.before) {
			t.Errorf("%s = %q (%v); want byte-identical to %q", c.path, got, err, c.before)
		}
		return
	}
	var top struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err != nil || json.Unmarshal(got, &top) != nil || top.Projects[cwd]["hasTrustDialogAccepted"] != true {
		t.Errorf("%s = %q (%v); want projects[%q].hasTrustDialogAccepted true", c.path, got, err, cwd)
	}
}

// ptrAssertResult parses a successful launch's stdout as JSON and fails
// unless its pre_trust is want; it returns the claude_instance_id.
func ptrAssertResult(t *testing.T, stdout, stderr string, code int, want string) string {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%q)", code, stderr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if got, ok := res["pre_trust"]; !ok || got != want {
		t.Errorf("pre_trust = %#v (present %v); want %q (stdout=%s)", got, ok, want, stdout)
	}
	id, _ := res["claude_instance_id"].(string)
	return id
}

// ptrAssertWarning fails unless stderr holds exactly one "pre-trust failed"
// line naming path when want is true, and none when it is false.
func ptrAssertWarning(t *testing.T, stderr, path string, want bool) {
	t.Helper()
	var lines []string
	for _, ln := range strings.Split(stderr, "\n") {
		if strings.Contains(ln, "pre-trust failed") {
			lines = append(lines, ln)
		}
	}
	if !want {
		if len(lines) != 0 {
			t.Errorf("stderr has pre-trust warnings %q; want none", lines)
		}
		return
	}
	if len(lines) != 1 || !strings.Contains(lines[0], path) {
		t.Errorf("stderr pre-trust warnings = %q; want exactly one naming %s (stderr=%q)", lines, path, stderr)
	}
}

// TestPreTrustResultCLISpawn: spawn prints pre_trust ok when the entry is
// written, failed (exit 0, one warning naming the file) when .claude.json is
// missing, and skipped with no warning under --no-pre-trust.
func TestPreTrustResultCLISpawn(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []ptrCase{
		{name: "claude.json present", present: true, want: "ok"},
		{name: "claude.json missing", present: false, want: "failed", wantWarn: true},
		{name: "no-pre-trust", present: true, optOut: true, want: "skipped", wantNPT: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			c := ptrSeedClaudeJSON(t, home, tc.present)
			cwd := t.TempDir()
			args := []string{"spawn", "--cwd", cwd}
			if tc.optOut {
				args = append(args, "--no-pre-trust")
			}
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			ptrAssertResult(t, stdout, stderr, code, tc.want)
			ptrAssertWarning(t, stderr, c.path, tc.wantWarn)
			c.check(t, cwd, tc.want)
			assertRecordedNoPreTrust(t, home, stdout, tc.wantNPT)
		})
	}
}

// TestPreTrustResultCLIResume: resume of a finished row prints pre_trust ok
// and writes the entry for an allowed row, failed (exit 0, one warning naming
// the file) when .claude.json is missing, and skipped with no warning and the
// file byte-identical for a row that records the opt-out.
func TestPreTrustResultCLIResume(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []ptrCase{
		{name: "allowed row", present: true, want: "ok"},
		{name: "claude.json missing", present: false, want: "failed", wantWarn: true},
		{name: "opted-out row", present: true, optOut: true, want: "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := t.TempDir()
			id := ptrSeedResumable(t, home, cwd, tc.optOut)
			c := ptrSeedClaudeJSON(t, home, tc.present)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "resume", "--claude-instance-id", id)
			if got := ptrAssertResult(t, stdout, stderr, code, tc.want); got != id {
				t.Errorf("claude_instance_id = %q; want %q", got, id)
			}
			ptrAssertWarning(t, stderr, c.path, tc.wantWarn)
			c.check(t, cwd, tc.want)
		})
	}
}

// ptrSeedResumable seeds an ended row under home at cwd with a session id and
// its transcript in home's ~/.claude, recording the socket under home's
// TMUX_TMPDIR (its per-user directory made 0700, as the launch requires) and,
// when optOut is true, the pre-trust opt-out. It returns the row's id.
func ptrSeedResumable(t *testing.T, home, cwd string, optOut bool) string {
	t.Helper()
	bootstrapDB(t, home)
	sockDir := filepath.Join(spawnTmuxTmpdir(t, home), fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	session := uuid.NewString()
	opts := []apitest.SpawnOption{
		apitest.WithTmuxSocket(filepath.Join(sockDir, "default")),
		apitest.WithJsonlPath(apitest.SeedJsonlUnder(t, filepath.Join(home, ".claude"), cwd, session)),
	}
	if optOut {
		opts = append(opts, apitest.WithNoPreTrust())
	}
	id, err := apitest.SeedSpawn(stateDB(home), "id-ptr-"+uuid.NewString()[:8], store.StateEnded, cwd, "", session, false, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return id
}
