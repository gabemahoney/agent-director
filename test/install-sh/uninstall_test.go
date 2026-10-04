package installsh_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestUninstallRemovesAdminBinary (b.vqr): uninstall.sh removes
// agent-director-admin, its .prior and install tempfiles, and the admin/
// directory, which it leaves, with a note, only when it holds other files.
func TestUninstallRemovesAdminBinary(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "uninstall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		other    bool // a file of someone else's in admin/
		wantLine string
	}{
		{"admin dir removed", false, "uninstall.sh: removed {admin}"},
		{"admin dir holding another file kept", true, "uninstall.sh: removed agent-director-admin; left {admin}, which holds other files"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".agent-director")
			admin := filepath.Join(root, "admin")
			files := []string{"bin/agent-director", "bin/agent-director.prior", "admin/agent-director-admin",
				"admin/agent-director-admin.prior", "admin/agent-director-admin.tmp.123", "state.db"}
			if tc.other {
				files = append(files, "admin/notes.txt")
			}
			for _, f := range files {
				p := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(f), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command("bash", script)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("uninstall.sh: %v\n%s", err, out)
			}

			want := strings.ReplaceAll(tc.wantLine, "{admin}", admin)
			if !slices.Contains(strings.Split(string(out), "\n"), want) {
				t.Errorf("output lacks the line %q:\n%s", want, out)
			}
			var left []string
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(root, p)
					left = append(left, rel)
				}
				return nil
			})
			wantLeft := []string{"state.db"}
			if tc.other {
				wantLeft = []string{"admin/notes.txt", "state.db"}
			}
			if !slices.Equal(left, wantLeft) {
				t.Errorf("files left = %q; want %q", left, wantLeft)
			}
			if _, err := os.Stat(admin); os.IsNotExist(err) == tc.other {
				t.Errorf("%s exists = %v; want %v", admin, !os.IsNotExist(err), tc.other)
			}
		})
	}
}
