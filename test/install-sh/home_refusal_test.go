package installsh_test

// install.sh and uninstall.sh build every path from HOME, so they stop first,
// before anything is read or written, when HOME is unset, empty or relative:
// exit 2, one message, nothing created (an empty HOME would otherwise put the
// install root at /.config/agent-director, a relative one under the cwd). The
// literal-follow of the message is advice_follow.sh's J32.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHomeRefusal(t *testing.T) {
	skills, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director"))
	if err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")

	homes := []struct {
		name string
		env  []string // HOME's own entry, if any
		show string   // what the message shows between HOME='' quotes
	}{
		{"unset", nil, ""},
		{"empty", []string{"HOME="}, ""},
		{"relative", []string{"HOME=relative/home"}, "relative/home"},
		{"dot", []string{"HOME=."}, "."},
		{"tilde", []string{"HOME=~"}, "~"},
	}
	for _, script := range []string{"install.sh", "uninstall.sh"} {
		for _, args := range [][]string{nil, {"--help"}} {
			for _, home := range homes {
				name := script + "/" + home.name
				if args != nil {
					name += "/help"
				}
				t.Run(name, func(t *testing.T) {
					scratch, err := filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					cwd := filepath.Join(scratch, "cwd")
					tmp := filepath.Join(scratch, "tmp")
					for _, d := range []string{cwd, tmp} {
						if err := os.Mkdir(d, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					rootBefore := statMark("/.config")
					rootAgent := statMark("/agent-director")

					// env -i: only PATH, TMPDIR and, for the case, HOME.
					cmd := exec.Command(bash, append([]string{filepath.Join(skills, script)}, args...)...)
					cmd.Dir = cwd
					cmd.Env = append([]string{"PATH=" + path, "TMPDIR=" + tmp}, home.env...)
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					code := 0
					var ee *exec.ExitError
					if err := cmd.Run(); errors.As(err, &ee) {
						code = ee.ExitCode()
					} else if err != nil {
						t.Fatal(err)
					}

					want := script + ": HOME is unset, empty or not an absolute path (HOME='" + home.show + "'). Run " + script +
						" with HOME set to the user's home directory, for example HOME=/home/alice.\n"
					if code != 2 {
						t.Errorf("exit %d, want 2", code)
					}
					if stderr.String() != want {
						t.Errorf("stderr = %q\nwant     %q", stderr.String(), want)
					}
					if stdout.Len() != 0 {
						t.Errorf("stdout = %q, want none", stdout.String())
					}
					for _, d := range []string{scratch, cwd, tmp} {
						entries, err := os.ReadDir(d)
						if err != nil {
							t.Fatal(err)
						}
						var names []string
						for _, e := range entries {
							names = append(names, e.Name())
						}
						// scratch holds the two directories this case made and nothing else.
						if d == scratch {
							if len(names) != 2 {
								t.Errorf("%s holds %v, want only cwd and tmp", d, names)
							}
							continue
						}
						if len(names) != 0 {
							t.Errorf("%s holds %v, want nothing created", d, names)
						}
					}
					if got := statMark("/.config"); got != rootBefore {
						t.Errorf("/.config was touched: before %q, after %q", rootBefore, got)
					}
					if got := statMark("/agent-director"); got != rootAgent {
						t.Errorf("/agent-director was touched: before %q, after %q", rootAgent, got)
					}
				})
			}
		}
	}
}

// statMark is "absent", or the path's modification time and the count of its
// entries, so a later change to it shows.
func statMark(p string) string {
	fi, err := os.Lstat(p)
	if err != nil {
		return "absent"
	}
	n := 0
	if fi.IsDir() {
		entries, _ := os.ReadDir(p)
		n = len(entries)
	}
	return fmt.Sprintf("%s, %d entries", fi.ModTime(), n)
}
