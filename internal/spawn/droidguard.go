package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/inputdelivery"
)

// DroidGuardCheckDeadline bounds both synthetic calls and their process-group
// cleanup together. On the AD test VM (linux/amd64, 2026-11-17), 20 pairs
// per case took 19.93–22.71 ms for a 300-byte command with four entries, and
// 20.54–23.29 ms for a 130212-byte command, using 70/72-byte stdin payloads.
// Two seconds gives over 85x the slowest pair for cold starts and host load,
// while bounding a stalled check (measurement: Subtask t3.jo7.ob.bd.fp).
const DroidGuardCheckDeadline = 2 * time.Second

// droidGuardCommandMaxBytes is Linux MAX_ARG_STRLEN. The terminating NUL also
// occupies one byte, so the command's text must be strictly shorter.
const droidGuardCommandMaxBytes = 128 << 10

// The Guard only emits one short fixed stderr line; 4 KiB leaves room for
// shell diagnostics without retaining unbounded helper output.
const droidGuardCheckOutputLimit = 4096

const droidGuardDoc = "docs/experimental.md#droid-secret-paths"

// Fixed problem kinds: no entry, pathname, OS error or helper output may
// become part of a launch refusal.
const (
	guardListMissing     = "no Path List File is configured"
	guardListUnreadable  = "the Path List File is not a readable regular file of at most 1 MiB"
	guardListEncoding    = "the Path List File is not valid UTF-8"
	guardListEmpty       = "the Path List has no entries"
	guardHome            = "HOME is not a usable absolute path"
	guardPath            = "agent-director's PATH is unset, empty or contains an empty or relative component"
	guardEntryWhitespace = "the entry has leading or trailing whitespace"
	guardEntryControl    = "the entry has a control character"
	guardEntryPath       = "the entry is not an absolute or ~/ path"
	guardProgram         = "the Guard program is unavailable"
	guardCommandLength   = "the Guard command exceeds the single-argument length limit"
	guardShell           = "sh is unavailable on agent-director's PATH"
	guardCheckFailed     = "the Guard self-check failed"
	guardCheckDeadline   = "the Guard self-check exceeded its deadline"
	guardCheckOutput     = "the Guard self-check output failed its bounds"
	guardCheckCleanup    = "the Guard self-check left processes that could not be stopped"
	guardUnchecked       = "the launch has no checked Guard"
)

// checkedDroidGuard is private to the launch boundary. Caller/template fields
// cannot manufacture a successful check or replace its command.
type checkedDroidGuard struct {
	entries []string
	command string
}

var (
	droidGuardProgramMu sync.Mutex
	droidGuardProgram   = installedGatewayBinary
)

// SetDroidGuardProgramForTest is the single program-selection seam for
// cross-package Droid launch fixtures. Normal launches never call it and use
// installedGatewayBinary. Tests install their droidfix stand-in and register
// restore with Cleanup; they must not overlap other tests changing this seam.
// It changes neither Cursor's selection nor config, environment or request
// schemas. Even a stand-in must pass the exact command's two self-checks.
func SetDroidGuardProgramForTest(selectProgram func() (string, error)) (restore func()) {
	droidGuardProgramMu.Lock()
	previous := droidGuardProgram
	droidGuardProgram = selectProgram
	droidGuardProgramMu.Unlock()
	return func() {
		droidGuardProgramMu.Lock()
		droidGuardProgram = previous
		droidGuardProgramMu.Unlock()
	}
}

func selectDroidGuardProgram() (string, error) {
	droidGuardProgramMu.Lock()
	selectProgram := droidGuardProgram
	droidGuardProgramMu.Unlock()
	return selectProgram()
}

// ResolveDroidGuard is the pre-write Guard step. Every Droid launch path must
// call it after request validation and launch-token assignment, before
// ResolveDroidRules, composition or any row, session, trust entry or launch
// file write, including finished-ID reuse of a row from before this feature.
// It reads the configured Path List once, checks entries, selects the Guard
// with installedGatewayBinary, composes its command and checks that exact
// command. Other agents return unchanged without any of that work. Only a
// successful check sets r's private checked data; no file contents are cached.
func ResolveDroidGuard(r Resolved, cfg config.Config) (Resolved, error) {
	return resolveDroidGuard(r, cfg, DroidGuardCheckDeadline)
}

// deadline is private so in-package tests can stop a hung self-check promptly.
func resolveDroidGuard(r Resolved, cfg config.Config, deadline time.Duration) (Resolved, error) {
	if r.Agent != config.AgentDroid {
		return r, nil
	}
	r.droidGuard = nil
	if cfg.Launch.DroidSecretPathsFile == "" {
		return r, droidGuardRefusal(0, guardListMissing)
	}
	data, err := readBoundedLaunchFile(cfg.Launch.DroidSecretPathsFile)
	if err != nil {
		return r, droidGuardRefusal(0, guardListUnreadable)
	}
	home, _ := os.UserHomeDir()
	entries, err := parseDroidSecretPaths(data, home)
	if err != nil {
		return r, err
	}
	program, err := selectDroidGuardProgram()
	if err != nil || !filepath.IsAbs(program) || strings.ContainsRune(program, 0) || !utf8.ValidString(program) {
		return r, droidGuardRefusal(0, guardProgram)
	}
	command, err := composeDroidGuardCommand(program, entries)
	if err != nil {
		return r, err
	}
	// Native lookup searches empty/relative components from the worker's
	// folder, while ResolveProgram skips them. Refuse every such component
	// so the self-check and the pane cannot select different shells.
	searchPath := os.Getenv("PATH")
	if searchPath == "" {
		return r, droidGuardRefusal(0, guardPath)
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if !filepath.IsAbs(dir) {
			return r, droidGuardRefusal(0, guardPath)
		}
	}
	// No caller extra_env enters this probe: sh and the wrapper's utilities
	// resolve on agent-director's own PATH, also set explicitly for the pane.
	shell, err := inputdelivery.ResolveProgram("sh", nil)
	if err != nil {
		return r, droidGuardRefusal(0, guardShell)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	if err := checkDroidGuard(ctx, shell, command); err != nil {
		return r, err
	}
	r.droidGuard = &checkedDroidGuard{entries: entries, command: command}
	return r, nil
}

func droidGuardRefusal(line int, problem string) error {
	where := ""
	if line > 0 {
		where = fmt.Sprintf("line %d: ", line)
	}
	return fmt.Errorf("%w: Droid launch Guard refused: %s%s; an operator must fix the Path List File named by [launch] droid_secret_paths_file or the Guard installation and environment (see %s); no row, session, trust entry or launch file was written and nothing was launched",
		ErrLaunchGuardRefused, where, problem, droidGuardDoc)
}

// parseDroidSecretPaths performs lexical validation only; it never follows a
// listed path. Physical line numbers survive ignored blank and comment lines.
func parseDroidSecretPaths(data []byte, home string) ([]string, error) {
	if !utf8.Valid(data) {
		return nil, droidGuardRefusal(0, guardListEncoding)
	}
	if !filepath.IsAbs(home) || !utf8.ValidString(home) || strings.ContainsFunc(home, unicode.IsControl) {
		return nil, droidGuardRefusal(0, guardHome)
	}
	var entries []string
	for index, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		problem := ""
		switch {
		case strings.TrimSpace(line) != line:
			problem = guardEntryWhitespace
		case strings.ContainsFunc(line, unicode.IsControl):
			problem = guardEntryControl
		case strings.HasPrefix(line, "~/"):
			line = filepath.Join(home, line[2:])
		case !filepath.IsAbs(line):
			problem = guardEntryPath
		}
		if problem != "" {
			return nil, droidGuardRefusal(index+1, problem)
		}
		entries = append(entries, filepath.Clean(line))
	}
	if len(entries) == 0 {
		return nil, droidGuardRefusal(0, guardListEmpty)
	}
	return entries, nil
}

// composeDroidGuardCommand quotes every argv word using the existing POSIX
// quoting helper. wc counts bytes, including NUL and trailing newlines; only
// its decimal count enters command substitution, never raw program output.
// A failed program adds a marker to that private stream, so a missing,
// signalled or nonzero program cannot look like silent success. The pipeline
// drains stdout completely, suppresses it, and leaves stderr untouched.
func composeDroidGuardCommand(program string, entries []string) (string, error) {
	words := append([]string{program, "tool-guard", "--"}, entries...)
	for i, word := range words {
		words[i] = statusShellQuote(word)
	}
	command := "bytes=$( { " + strings.Join(words, " ") +
		"; result=$?; if [ \"$result\" -ne 0 ]; then printf x; fi; } | wc -c) || exit 2; " +
		"[ \"$bytes\" -eq 0 ] 2>/dev/null || exit 2; exit 0"
	if len(command) >= droidGuardCommandMaxBytes {
		return "", droidGuardRefusal(0, guardCommandLength)
	}
	return command, nil
}

// checkDroidGuard runs exactly the overlay command twice, sharing one budget.
// It deliberately judges only status and output bounds; helper diagnostics
// are neither parsed nor echoed. ctx is also the deterministic timeout seam.
func checkDroidGuard(ctx context.Context, shell, command string) error {
	for _, check := range []struct {
		payload string
		exit    int
	}{
		{`{"hook_event_name":"PreToolUse","tool_name":"AskUser","tool_input":{}}`, 2},
		{`{"hook_event_name":"PreToolUse","tool_name":"TodoWrite","tool_input":{}}`, 0},
	} {
		probe := inputdelivery.ProbeCommand(ctx, shell, []string{"-c", command}, nil, DroidForbiddenEnvKeys)
		probe.Stdin = strings.NewReader(check.payload)
		stdout := inputdelivery.ProbeOutput{Limit: droidGuardCheckOutputLimit}
		stderr := inputdelivery.ProbeOutput{Limit: droidGuardCheckOutputLimit}
		probe.Stdout, probe.Stderr = &stdout, &stderr
		runErr, stopErr := inputdelivery.RunProbe(probe)
		problem := ""
		var exitErr *exec.ExitError
		exit := 0
		switch {
		case ctx.Err() != nil:
			problem = guardCheckDeadline
		case stdout.Err() != nil || stderr.Err() != nil || stdout.String() != "":
			problem = guardCheckOutput
		case runErr == nil:
		case errors.As(runErr, &exitErr):
			exit = exitErr.ExitCode()
		default:
			problem = guardCheckFailed
		}
		if problem == "" && exit != check.exit {
			problem = guardCheckFailed
		}
		if stopErr != nil {
			problem = joinProblems(problem, guardCheckCleanup)
		}
		if problem != "" {
			return droidGuardRefusal(0, problem)
		}
	}
	return nil
}
