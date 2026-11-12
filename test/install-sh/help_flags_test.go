package installsh_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestHelpFlags (R3): a flag install.sh's argument loop accepts and --help
// doesn't list fails here.
func TestHelpFlags(t *testing.T) {
	t.Parallel()
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	loop := regexp.MustCompile(`(?ms)^while \[\[ \$# -gt 0 \]\]; do\n    case "\$1" in\n(.*?)^    esac\ndone$`).FindStringSubmatch(string(data))
	if loop == nil {
		t.Fatal(`install.sh has no argument loop "while [[ $# -gt 0 ]]; do / case \"$1\" in … esac / done"`)
	}
	var flags []string
	for _, arm := range regexp.MustCompile(`(?m)^        ([^ ()]+)\)$`).FindAllStringSubmatch(loop[1], -1) {
		for _, label := range strings.Split(arm[1], "|") {
			if strings.HasPrefix(label, "-") {
				flags = append(flags, label)
			}
		}
	}
	if len(flags) == 0 {
		t.Fatal("install.sh's argument loop has no -x or --x labels")
	}

	home := t.TempDir()
	cmd := exec.Command("bash", script, "--help")
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir(), "HOME=" + home}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("install.sh --help: %v, stderr %q; want exit 0 and no stderr", err, stderr.String())
	}
	help := stdout.String()
	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			if !regexp.MustCompile(`(^|[^-a-z0-9])` + regexp.QuoteMeta(flag) + `($|[^-a-z0-9])`).MatchString(help) {
				t.Errorf("install.sh accepts %s, but --help doesn't list it", flag)
			}
		})
	}
}
