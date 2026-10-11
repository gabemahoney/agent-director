package main_test

import (
	"encoding/json"
	"testing"
)

// configDirValue is the literal both binaries' version results report as
// config_dir, whatever HOME is (SR-11.7): no path is resolved.
const configDirValue = "~/.config/agent-director"

// TestEnvelopeParityVersion asserts the version envelope shape: exactly the
// three JSON string fields "version", "commit" and "config_dir" (version and
// commit may be empty in test builds; presence is the invariant; config_dir is
// the literal ~/.config/agent-director).
func TestEnvelopeParityVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	_, v := parsed["version"].(string)
	_, c := parsed["commit"].(string)
	dir, d := parsed["config_dir"].(string)
	if !v || !c || !d || len(parsed) != 3 {
		t.Errorf("version output = %v; want exactly the string keys version, commit and config_dir", parsed)
	}
	if dir != configDirValue {
		t.Errorf("config_dir = %q; want the literal %q", dir, configDirValue)
	}
}

// TestVersionConfigDirIsTheSameForEveryHome: config_dir is the literal under
// an empty HOME and under another one, so it reads no home and no directory.
func TestVersionConfigDirIsTheSameForEveryHome(t *testing.T) {
	var outs []string
	for _, home := range []string{"", t.TempDir()} {
		stdout, stderr, code := runCLIWithHome(t, home, "version")
		if code != 0 || stderr != "" {
			t.Fatalf("HOME=%q: exit=%d stderr=%q; want 0 and empty", home, code, stderr)
		}
		var parsed struct {
			ConfigDir string `json:"config_dir"`
		}
		if err := json.Unmarshal([]byte(stdout), &parsed); err != nil || parsed.ConfigDir != configDirValue {
			t.Errorf("HOME=%q: version = %q (err %v); want config_dir %q", home, stdout, err, configDirValue)
		}
		outs = append(outs, stdout)
	}
	if outs[0] != outs[1] {
		t.Errorf("version differs by HOME: %q vs %q", outs[0], outs[1])
	}
}

// TestMCPVersionToolCarriesConfigDir: serve's `version` tool reply holds the
// same config_dir the verb prints.
func TestMCPVersionToolCarriesConfigDir(t *testing.T) {
	srv := startServe(t, t.TempDir())
	t.Cleanup(srv.kill)
	srv.initialize(t)

	text := srv.callToolOK(t, "version", `{}`)
	srv.stop(t)

	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("version tool text %q is not JSON: %v", text, err)
	}
	if res["config_dir"] != configDirValue || len(res) != 3 {
		t.Errorf("version tool result = %v; want exactly version, commit and config_dir %q", res, configDirValue)
	}
}

// TestEnvelopeParityListEmpty asserts the list envelope against an empty
// store is exactly {"spawns":[]}: a non-null empty array, never null or absent.
func TestEnvelopeParityListEmpty(t *testing.T) {
	stdout, stderr, code := runCLI(t, "list")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	if stdout != "{\"spawns\":[]}\n" {
		t.Errorf("stdout = %q; want {\"spawns\":[]}", stdout)
	}
}

// TestEnvelopeParitySpawnCwdNotFound asserts the spawn-failure envelope for a
// nonexistent cwd: exit code 1, empty stdout, and an ErrCwdNotFound envelope
// with a description.
func TestEnvelopeParitySpawnCwdNotFound(t *testing.T) {
	stdout, stderr, code := runCLI(t, withAgent("claude", "spawn", "--cwd", "/no/such/path")...)
	if env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrCwdNotFound"); env.ErrDescription == "" {
		t.Errorf("err_description empty; want a human-readable message")
	}
}
