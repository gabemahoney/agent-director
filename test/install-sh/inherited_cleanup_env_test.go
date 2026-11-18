package installsh_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// b.f6i: EXIT cleanup must preserve caller-owned paths inherited through the
// environment, both after capture 2's file-only upgrade and on an early refusal.
func TestInstallInheritedCleanupPaths(t *testing.T) {
	for _, path := range []struct {
		name string
		dir  bool
	}{
		{name: "TMP"},
		{name: "TMP", dir: true}, // Capture 2 reported success, then rm failed on this directory.
		{name: "ADMIN_TMP"},
		{name: "tmp_bin"},
		{name: "tmp_admin"},
		{name: "tmp_sentinel"},
		{name: "stage_bin"},
		{name: "stage_admin"},
		{name: "migrate_tmp"},
		{name: "link_tmp"},
		{name: "version_out"},
		{name: "version_err"},
		{name: "sched_tmp", dir: true},
	} {
		name := path.name
		if path.dir {
			name += "-directory"
		}
		phases := []string{"file-only-upgrade", "staging-refusal"}
		if path.name == "stage_bin" || path.name == "stage_admin" {
			// These names are already cleared before the later staging trap;
			// b.f6i also requires ownership before the earlier download trap.
			phases = append(phases, "release-allocation-refusal")
		}
		for _, phase := range phases {
			t.Run(name+"/"+phase, func(t *testing.T) {
				h := newMoveHome(t)
				if phase == "release-allocation-refusal" {
					h.Pair = movePair{} // --from-release must receive no local binary arguments.
					h.Flags = []string{"--from-release", "v0.99.0", "--no-hooks", "--no-symlink"}
					h.WriteTool("curl", "echo 'unexpected curl invocation' >&2; exit 99")
					h.WrapTool("mktemp", `if [[ "$1" == /tmp/agent-director.XXXXXX ]]; then
    echo 'injected release allocation failure' >&2
    exit 73
fi`)
				} else {
					moveMustInstall(t, h) // Establish the published layout with an untainted environment.
					h.Flags = []string{"--no-hooks", "--keep-prior", "--symlink-dir", filepath.Join(h.Home, ".local", "bin")}
					if phase == "staging-refusal" {
						// Fail allocation after EXIT cleanup is armed but before
						// publication assigns its own output/error files.
						h.WrapTool("mktemp", `[[ "$*" != *agent-director.stage.* ]] || exit 1`)
					}
				}
				inherited := filepath.Join(h.Root, "caller-owned", name)
				sentinel := inherited
				if path.dir {
					sentinel = filepath.Join(inherited, "sentinel")
				}
				const content = "caller data: preserve exactly\n"
				h.Write(sentinel, content, 0600)
				h.Env[path.name] = inherited

				got := h.Install()
				switch phase {
				case "release-allocation-refusal":
					asset := "agent-director-" + runtime.GOOS + "-" + runtime.GOARCH
					admin := "agent-director-admin-" + runtime.GOOS + "-" + runtime.GOARCH
					want := "  release : gabemahoney/agent-director @ v0.99.0 (" + asset + ", " + admin + ")\n"
					if got.Code != 73 || !strings.HasSuffix(got.Stdout, want) || got.Stderr != "injected release allocation failure\n" {
						t.Errorf("release allocation refusal = %+v; want exit 73, stdout ending in %q and only the injected diagnostic", got, want)
					}
					outcomeLines(t, got.Stdout, "install.sh: pre-flight OK")
				case "staging-refusal":
					want := "install.sh: cannot make a temp file to hold a copy of " + h.Pair.Main + "\n"
					if got.Code != 3 || got.Stderr != want {
						t.Errorf("staging refusal = %+v; want exit 3 and stderr %q", got, want)
					}
					outcomeLines(t, got.Stdout, "install.sh: pre-flight OK")
					if strings.Contains(got.Stdout, "  schema  :") || strings.Contains(got.Stdout, "install.sh: done.") {
						t.Errorf("staging refusal reported publication or success: %q", got.Stdout)
					}
				case "file-only-upgrade":
					if got.Code != 0 {
						t.Errorf("file-only upgrade = %+v; want exit 0", got)
					}
					outcomeLines(t, got.Stdout, "  schema  : not inspected (file-only upgrade)")
					if !strings.HasSuffix(got.Stdout, "install.sh: done. Try: "+filepath.Join(h.New, "bin", "agent-director")+" help\n") {
						t.Errorf("file-only upgrade did not finish: %q", got.Stdout)
					}
					if strings.Contains(got.Stderr, inherited) {
						t.Errorf("cleanup diagnosed caller-owned path %s: %s", inherited, got.Stderr)
					}
				}
				if data, err := os.ReadFile(sentinel); err != nil || string(data) != content {
					t.Errorf("installer removed or changed inherited %s path %s: got %q, err %v; want %q", path.name, sentinel, data, err, content)
				}
			})
		}
	}
}
