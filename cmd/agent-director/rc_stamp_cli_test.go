package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// rcStampValue is the release-candidate stamp SR-20.6 defines: the TypeScript
// package's version plus "-rc.1".
func rcStampValue(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "pkg", "ts-bun-client", "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil || pkg.Version == "" {
		t.Fatalf("package.json version = %q (err %v); want a version", pkg.Version, err)
	}
	return pkg.Version + "-rc.1"
}

// buildRCBinary stamps value through the Makefile's version override into a
// temp dir (never ./bin) and returns this host's binary.
func buildRCBinary(t *testing.T, root, value string) string {
	t.Helper()
	dist := t.TempDir()
	cmd := exec.Command("make", "release-binaries")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "AGENT_DIRECTOR_BUILD_VERSION="+value, "RELEASE_DIST_DIR="+dist)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make release-binaries (AGENT_DIRECTOR_BUILD_VERSION=%s): %v\n%s", value, err, out)
	}
	return filepath.Join(dist, "agent-director-"+runtime.GOOS+"-"+runtime.GOARCH)
}

// releaseTargets are the GOOS/GOARCH pairs `make release-binaries` builds.
var releaseTargets = []string{"linux/amd64", "linux/arm64", "darwin/arm64"}

// decodeVersion parses a `version` result and returns its version field.
func decodeVersion(t *testing.T, raw string) string {
	t.Helper()
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("version result is not JSON: %v\nraw=%q", err, raw)
	}
	return v.Version
}

// TestRCStampReportedByVersionVerbAndMCPTool pins AC-REL-03: an RC-stamped
// build reports its X.Y.Z-rc.N value byte for byte through `version` and MCP.
func TestRCStampReportedByVersionVerbAndMCPTool(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make release-binaries")
	}
	if host := runtime.GOOS + "/" + runtime.GOARCH; !slices.Contains(releaseTargets, host) {
		t.Skipf("release-binaries builds %v only; host is %s", releaseTargets, host)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	want := rcStampValue(t, root)
	bin := buildRCBinary(t, root, want)

	t.Run("version verb", func(t *testing.T) {
		stdout, stderr, code := runBinWithHome(t, bin, t.TempDir(), "version")
		if code != 0 || stderr != "" {
			t.Fatalf("version exit=%d stderr=%q; want exit 0 and empty stderr", code, stderr)
		}
		if got := decodeVersion(t, stdout); got != want {
			t.Fatalf("version = %q; want %q", got, want)
		}
	})

	t.Run("MCP version tool", func(t *testing.T) {
		s := startServeBin(t, bin, t.TempDir())
		t.Cleanup(s.kill)
		s.initialize(t)
		r := s.request(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"version","arguments":{}}}`+"\n")
		if r.Error != nil || r.Result == nil || len(r.Result.Content) != 1 {
			t.Fatalf("version tool reply = %+v; want one text part", r)
		}
		if got := decodeVersion(t, r.Result.Content[0].Text); got != want {
			t.Fatalf("MCP version = %q; want %q", got, want)
		}
	})
}
