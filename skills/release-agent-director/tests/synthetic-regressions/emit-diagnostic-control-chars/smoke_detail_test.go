package emitdiagnosticcontrolchars_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSmokeDetailEscapedOnce (b.nsa): per-binary-smoke.sh's host-exec detail
// decodes to the raw `help` output. It was escaped by hand and again by jq, so
// quotes and backslashes came back escaped, and output over 128 KiB lost the sub-check.
func TestSmokeDetailEscapedOnce(t *testing.T) {
	t.Parallel()
	requireJQ(t)
	host := runtime.GOOS + "-" + runtime.GOARCH
	switch host {
	case "linux-amd64", "linux-arm64", "darwin-arm64":
	default:
		t.Skipf("per-binary-smoke.sh checks no binary for host %s", host)
	}
	dist := t.TempDir()
	for _, name := range []string{"agent-director-" + host, "agent-director-admin-" + host} {
		fakeFailingBin(t, dist, name, longTail+"\n")
	}
	want := map[string]string{
		"smoke." + host + ".host-exec":       "args=[help] " + longTail, // its first five lines
		"smoke.admin-" + host + ".host-exec": "args=[help] " + oddLine,  // its first line
	}

	cmd := exec.Command("bash", filepath.Join(gatesDir(t), "smoke", "per-binary-smoke.sh"), t.TempDir())
	cmd.Env = append(os.Environ(), "SMOKE_DIST_DIR="+dist)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_ = cmd.Run() // exits 1: the fakes fail and the other platforms' binaries are absent
	var out struct {
		SubChecks []struct {
			Name   string `json:"name"`
			Detail string `json:"detail"`
		} `json:"sub_checks"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
		t.Fatalf("smoke stdout is not valid JSON: %v\nstdout: %.2000q\nstderr: %.2000q", err, stdout.String(), stderr.String())
	}
	for _, sc := range out.SubChecks {
		w, ok := want[sc.Name]
		if !ok {
			continue
		}
		delete(want, sc.Name)
		if sc.Detail != w {
			t.Errorf("%s detail (%d bytes) is not the raw output (%d bytes); starts %.200q",
				sc.Name, len(sc.Detail), len(w), sc.Detail)
		}
	}
	for name := range want {
		t.Errorf("sub-check %s is missing from the smoke output", name)
	}
}
