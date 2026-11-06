package part

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// Tools is what a part needs of the Mac's tools. OSTools is the real one;
// tests fake it, so no test depends on the host's tools.
type Tools interface {
	// LookPath returns the first executable regular file named name in the
	// directories of path, a PATH value.
	LookPath(name, path string) (string, error)
	// IsExecutable reports whether path is an executable regular file.
	IsExecutable(path string) bool
	// Output runs path with args in macrun's own environment and returns its
	// stdout, trimmed. The error names the command and holds its stderr.
	Output(ctx context.Context, path string, args ...string) (string, error)
}

// OSTools is the Tools of the machine macrun runs on.
type OSTools struct{}

// LookPath implements Tools. An empty PATH entry is skipped rather than read
// as the current directory.
func (t OSTools) LookPath(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if candidate := filepath.Join(dir, name); t.IsExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not on PATH %s", name, path)
}

// IsExecutable implements Tools.
func (OSTools) IsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// Output implements Tools.
func (OSTools) Output(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// systemPath is macOS's own part of the tests' PATH, which comes first.
var systemPath = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// The tools the tests' PATH must resolve to macOS's own.
const (
	systemBash    = "/bin/bash"
	systemSQLite3 = "/usr/bin/sqlite3"
	swVers        = "/usr/bin/sw_vers"
)

// minBash is the oldest bash the tests may use: the driver's scripts use
// mapfile and associative arrays.
var minBash = [2]int{4, 4}

// The variables the part sets for the tests, and the one it removes so the
// envelope-diff suite builds its own CLI instead of skipping.
const (
	envTestBash    = "AGENT_DIRECTOR_TEST_BASH"
	envTestSQLite3 = "AGENT_DIRECTOR_TEST_SQLITE3"
	envTestBinary  = "AGENT_DIRECTOR_TEST_BINARY"
	envPath        = "PATH"
)

// agentStatePaths are the home paths whose presence means the Mac holds a
// real agent-director store or agent account, so no case may run there.
var agentStatePaths = []string{".config/agent-director", ".agent-director", ".claude", ".codex"}

// githubHosted is runner.environment on a GitHub-hosted runner.
const githubHosted = "github-hosted"

// checkSafety checks that the part runs on a GitHub-hosted runner whose
// home holds no agent state. A dangling link counts as present.
func checkSafety(runnerEnvironment, home string) *model.SetupFailure {
	if runnerEnvironment != githubHosted {
		return &model.SetupFailure{Step: CheckRunner,
			Message: fmt.Sprintf("runner.environment is %q, not %q; a Mac Run runs only on GitHub-hosted runners", runnerEnvironment, githubHosted)}
	}
	for _, rel := range agentStatePaths {
		path := filepath.Join(home, filepath.FromSlash(rel))
		_, err := os.Lstat(path)
		if err == nil {
			return &model.SetupFailure{Step: CheckHome,
				Message: fmt.Sprintf("%s exists, so this Mac may hold a real agent-director store or agent account", path)}
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return &model.SetupFailure{Step: CheckHome, Message: fmt.Sprintf("can't check %s: %v", path, err)}
		}
	}
	return nil
}

// toolPaths are the tools a part found.
type toolPaths struct {
	bash, sqlite3, goCmd, bun, tmux string
	// testsPath is the tests' PATH.
	testsPath string
}

// findTools finds go and bun on macrun's own PATH, builds the tests' PATH,
// and checks that on it bash and sqlite3 are macOS's own, go and bun the ones
// found, and tmux present, and that bash and sqlite3 are executables by
// absolute path, bash 4.4 or later.
func findTools(ctx context.Context, t Tools, opts Options) (toolPaths, *model.SetupFailure) {
	fail := func(format string, args ...any) (toolPaths, *model.SetupFailure) {
		return toolPaths{}, &model.SetupFailure{Step: StepTools, Message: fmt.Sprintf(format, args...)}
	}
	for _, tool := range []struct{ flag, path string }{{"-bash", opts.Bash}, {"-sqlite3", opts.SQLite3}} {
		if !filepath.IsAbs(tool.path) || !t.IsExecutable(tool.path) {
			return fail("%s %q is not an executable by absolute path", tool.flag, tool.path)
		}
	}
	ownPath := lookupEnv(opts.Environ, envPath)
	goCmd, err := t.LookPath("go", ownPath)
	if err != nil {
		return fail("go: %v", err)
	}
	bun, err := t.LookPath("bun", ownPath)
	if err != nil {
		return fail("bun: %v", err)
	}
	dirs := append(append([]string{}, systemPath...),
		filepath.Dir(goCmd), filepath.Dir(bun),
		filepath.Join(opts.Homebrew, "bin"), filepath.Join(opts.Homebrew, "sbin"))
	tp := toolPaths{bash: opts.Bash, sqlite3: opts.SQLite3, goCmd: goCmd, bun: bun, testsPath: strings.Join(dirs, string(filepath.ListSeparator))}
	for _, want := range []struct{ name, path string }{
		{"bash", systemBash}, {"sqlite3", systemSQLite3}, {"go", goCmd}, {"bun", bun},
	} {
		got, err := t.LookPath(want.name, tp.testsPath)
		if err != nil {
			return fail("%s: %v", want.name, err)
		}
		if got != want.path {
			return fail("%s resolves to %s on the tests' PATH, not %s", want.name, got, want.path)
		}
	}
	if tp.tmux, err = t.LookPath("tmux", tp.testsPath); err != nil {
		return fail("tmux: %v", err)
	}
	info, err := t.Output(ctx, opts.Bash, "-c", `echo "${BASH_VERSINFO[0]} ${BASH_VERSINFO[1]}"`)
	if err != nil {
		return fail("bash version: %v", err)
	}
	if !atLeast(info, minBash) {
		return fail("-bash %s is bash %q, older than %d.%d", opts.Bash, info, minBash[0], minBash[1])
	}
	return tp, nil
}

// atLeast reports whether info, "<major> <minor>", is at least want.
func atLeast(info string, want [2]int) bool {
	fields := strings.Fields(info)
	if len(fields) != 2 {
		return false
	}
	major, err1 := strconv.Atoi(fields[0])
	minor, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > want[0] || major == want[0] && minor >= want[1]
}

// versions records SR-3.4's versions, in the order of the model's names.
func versions(ctx context.Context, t Tools, tp toolPaths) ([]model.Version, *model.SetupFailure) {
	var out []model.Version
	for _, v := range []struct {
		name string
		path string
		args []string
	}{
		{model.VersionMacOSProduct, swVers, []string{"-productVersion"}},
		{model.VersionMacOSBuild, swVers, []string{"-buildVersion"}},
		{model.VersionTmux, tp.tmux, []string{"-V"}},
		{model.VersionMacOSSQLite3, systemSQLite3, []string{"--version"}},
		{model.VersionGo, tp.goCmd, []string{"env", "GOVERSION"}},
		{model.VersionBun, tp.bun, []string{"--version"}},
		{model.VersionBash, tp.bash, []string{"--version"}},
		{model.VersionStandardSQLite3, tp.sqlite3, []string{"--version"}},
	} {
		value, err := t.Output(ctx, v.path, v.args...)
		if err != nil {
			return nil, &model.SetupFailure{Step: StepVersions, Message: fmt.Sprintf("%s: %v", v.name, err)}
		}
		value, _, _ = strings.Cut(value, "\n")
		out = append(out, model.Version{Name: v.name, Value: strings.TrimSpace(value)})
	}
	return out, nil
}

// testsEnv returns macrun's own environment with the tests' PATH, the two
// tool variables set, and AGENT_DIRECTOR_TEST_BINARY removed. Everything
// else passes through.
func testsEnv(environ []string, tp toolPaths) []string {
	drop := map[string]bool{envPath: true, envTestBash: true, envTestSQLite3: true, envTestBinary: true}
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	return append(env, envPath+"="+tp.testsPath, envTestBash+"="+tp.bash, envTestSQLite3+"="+tp.sqlite3)
}

// lookupEnv returns name's value in environ, the last one if it repeats.
func lookupEnv(environ []string, name string) string {
	value := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value = v
		}
	}
	return value
}
