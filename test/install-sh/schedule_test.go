package installsh_test

// install.sh's schedule step (b.wur): the find-missing and expire sweeps have
// to be scheduled for the row states to stay honest, and an install that sets
// none up says so. The cases drive install.sh against stand-ins for crontab,
// systemctl and curl (the INSTALL_SH_TEST_CRONTAB, _SYSTEMCTL and
// _CURL_OVERRIDE switches) and read what it prints and writes: the stand-ins'
// state and logs, the unit files, stdout and stderr. The uninstall side is in
// schedule_uninstall_test.go, which uses this file's helpers.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const (
	schedMark = "agent-director-schedule"
	// schedWarnHead is the first line of the warning an install ends with when
	// none of its entries is scheduled.
	schedWarnHead = "install.sh: WARNING: nothing schedules agent-director's find-missing and expire sweeps on this machine."
	schedRawBase  = "https://raw.githubusercontent.com/gabemahoney/agent-director/"
	// schedFoundCron and schedFoundSystemd are the stdout lines of an install
	// that finds its entries in place and leaves them.
	schedFoundCron    = "  schedule: found this install's cron entries for find-missing and expire; left as they are"
	schedFoundSystemd = "  schedule: found this install's systemd entries for find-missing and expire; left as they are"
)

var schedFiles = []string{
	"agent-director.cron",
	"agent-director-find-missing.service", "agent-director-find-missing.timer",
	"agent-director-expire.service", "agent-director-expire.timer",
}

// copyDir copies the files of src (one level) into dst.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// schedCronStub is crontab: the user's crontab is the file CRON_STUB_FILE (none
// until written, which `-l` reports as real crontabs do), every call is logged,
// and CRON_STUB_LIST_FAIL and CRON_STUB_WRITE_FAIL make `-l` and `-` fail with
// that message, and CRON_STUB_NOFILE_MSG is what `-l` says when there is no
// crontab yet (busybox says "can't open '<user>': No such file or directory").
const schedCronStub = `echo "$*" >>"${CRON_STUB_LOG:?}"
case "$1" in
-l)
    if [[ -n "${CRON_STUB_LIST_FAIL:-}" ]]; then echo "$CRON_STUB_LIST_FAIL" >&2; exit 1; fi
    [[ -f "$CRON_STUB_FILE" ]] || { echo "${CRON_STUB_NOFILE_MSG:-no crontab for tester}" >&2; exit 1; }
    cat "$CRON_STUB_FILE" ;;
-)
    if [[ -n "${CRON_STUB_WRITE_FAIL:-}" ]]; then cat >/dev/null; echo "$CRON_STUB_WRITE_FAIL" >&2; exit 1; fi
    cat >"$CRON_STUB_FILE" ;;
-r) rm -f "$CRON_STUB_FILE" ;;
*) echo "usage: crontab [-l | - | -r]" >&2; exit 2 ;;
esac`

// schedSystemctlStub is systemctl --user: every call is logged, show-environment
// exits SYSCTL_STUB_SHOW_RC (0) and prints the lines of SYSCTL_STUB_ENV (the
// user manager's environment: none unless a case sets it), a call with the word
// SYSCTL_STUB_FAIL fails, and enable and disable make and remove the
// timers.target.wants links in the manager's unit directory: its XDG_CONFIG_HOME's
// systemd/user, else its HOME's .config/systemd/user, else this shell's own rule (an absolute XDG_CONFIG_HOME, else HOME).
const schedSystemctlStub = `echo "$*" >>"${SYSCTL_STUB_LOG:?}"
unit_dir="$HOME/.config/systemd/user" mgr_xdg="" mgr_home=""
while IFS= read -r l; do
    case "$l" in XDG_CONFIG_HOME=/*) mgr_xdg="${l#*=}" ;; HOME=/*) mgr_home="${l#*=}" ;; esac
done <<<"${SYSCTL_STUB_ENV:-}"
case "${XDG_CONFIG_HOME:-}" in /*) unit_dir="$XDG_CONFIG_HOME/systemd/user" ;; esac
if [[ -n "$mgr_xdg" ]]; then unit_dir="$mgr_xdg/systemd/user"; elif [[ -n "$mgr_home" ]]; then unit_dir="$mgr_home/.config/systemd/user"; fi
wants="$unit_dir/timers.target.wants"
if [[ "$1 $2" == "--user show-environment" ]]; then
    [[ -z "${SYSCTL_STUB_ENV:-}" ]] || printf '%s\n' "$SYSCTL_STUB_ENV"
    exit "${SYSCTL_STUB_SHOW_RC:-0}"
fi
if [[ -n "${SYSCTL_STUB_FAIL:-}" && " $* " == *" $SYSCTL_STUB_FAIL "* ]]; then exit 1; fi
case "$2" in
enable) mkdir -p "$wants"; for u in "${@:3}"; do ln -sf "../$u" "$wants/$u"; done ;;
disable) for u in "${@:4}"; do rm -f "$wants/$u"; done ;;
esac
exit 0`

// schedCurlStub answers the raw.githubusercontent.com GETs for the schedule
// files from CURL_STUB_DIR, and the release downloads with CURL_STUB_BIN and
// CURL_STUB_ADMIN; every URL is logged. CURL_STUB_404 names a file that answers
// 404, CURL_STUB_HTML one that answers 200 with a page that is not a schedule
// file, and CURL_STUB_404_REFS the repository refs (space-separated) at which
// every schedule file answers 404.
const schedCurlStub = `out="" url=""
while [[ $# -gt 0 ]]; do
    case "$1" in -o) out="$2"; shift 2 ;; -w|--retry|--connect-timeout|--max-time) shift 2 ;; -*) shift ;; *) url="$1"; shift ;; esac
done
echo "$url" >>"${CURL_STUB_LOG:?}"
name="${url##*/}"
case "$url" in
https://raw.githubusercontent.com/*)
    ref="${url#*agent-director/}"; ref="${ref%%/skills/*}"
    if [[ " ${CURL_STUB_404_REFS:-} " == *" $ref "* ]]; then : >"$out"; printf 404; exit 22; fi ;;
esac
if [[ "$name" == "${CURL_STUB_404:-}" ]]; then : >"$out"; printf 404; exit 22; fi
if [[ "$name" == "${CURL_STUB_HTML:-}" ]]; then echo "<html><body>Not Found</body></html>" >"$out"; printf 200; exit 0; fi
case "$url" in
*/releases/download/*admin*) cp "$CURL_STUB_ADMIN" "$out" ;;
*/releases/download/*) cp "$CURL_STUB_BIN" "$out" ;;
*) cp "$CURL_STUB_DIR/$name" "$out" 2>/dev/null || { : >"$out"; printf 404; exit 22; } ;;
esac
printf 200`

// schedCron puts the stand-in crontab in place for the case: its crontab is
// the file crontab, empty (no crontab) unless text is not "", and its calls go
// to the log, which the returned functions read.
type schedStub struct{ h *moveHome }

func (s schedStub) path(name string) string { return filepath.Join(s.h.Root, name) }

// Cron installs the stand-in crontab holding text ("" is no crontab at all).
func (s schedStub) Cron(text string) {
	s.h.WriteTool("crontab-stub", schedCronStub)
	s.h.Env["INSTALL_SH_TEST_CRONTAB"] = filepath.Join(s.h.Tools, "crontab-stub")
	s.h.Env["CRON_STUB_FILE"] = s.path("crontab.txt")
	s.h.Env["CRON_STUB_LOG"] = s.path("crontab.log")
	if text != "" {
		s.h.Write(s.path("crontab.txt"), text, 0o644)
	}
}

// Crontab is the stand-in crontab's text ("" and false when there is none).
func (s schedStub) Crontab() (string, bool) {
	b, err := os.ReadFile(s.path("crontab.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	} else if err != nil {
		s.h.t.Fatal(err)
	}
	return string(b), true
}

// CronCalls is the arguments of each call the stand-in crontab got.
func (s schedStub) CronCalls() []string { return s.calls("crontab.log") }

// CronWrites is how many times it was given a new crontab (`crontab -`).
func (s schedStub) CronWrites() int {
	n := 0
	for _, c := range s.CronCalls() {
		if c == "-" {
			n++
		}
	}
	return n
}

// Systemctl installs the stand-in systemctl; its user manager answers unless
// showRC is not 0.
func (s schedStub) Systemctl(showRC int) {
	s.h.WriteTool("systemctl-stub", schedSystemctlStub)
	s.h.Env["INSTALL_SH_TEST_SYSTEMCTL"] = filepath.Join(s.h.Tools, "systemctl-stub")
	s.h.Env["SYSCTL_STUB_LOG"] = s.path("systemctl.log")
	s.h.Env["SYSCTL_STUB_SHOW_RC"] = fmt.Sprint(showRC)
}

// ManagerEnv sets the user manager's environment its show-environment prints:
// each of lines is one NAME=value.
func (s schedStub) ManagerEnv(lines ...string) {
	s.h.Env["SYSCTL_STUB_ENV"] = strings.Join(lines, "\n")
}

// SystemctlCalls is the arguments of each call the stand-in systemctl got.
func (s schedStub) SystemctlCalls() []string { return s.calls("systemctl.log") }

// Curl installs the stand-in curl serving the repo's schedule files and the
// case's binaries.
func (s schedStub) Curl() {
	s.h.WriteTool("curl-stub", schedCurlStub)
	s.h.Env["INSTALL_SH_TEST_CURL_OVERRIDE"] = filepath.Join(s.h.Tools, "curl-stub")
	s.h.Env["CURL_STUB_LOG"] = s.path("curl.log")
	s.h.Env["CURL_STUB_DIR"] = s.h.S.ScheduleSrc
	s.h.Env["CURL_STUB_BIN"] = s.h.Pair.Main
	s.h.Env["CURL_STUB_ADMIN"] = s.h.Pair.Admin
}

// CurlURLs is every URL the stand-in curl was asked for.
func (s schedStub) CurlURLs() []string { return s.calls("curl.log") }

func (s schedStub) calls(log string) []string {
	b, err := os.ReadFile(s.path(log))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		s.h.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// schedScript copies install.sh to a place of its own under the case's Root
// (schedule/ beside it when withFiles) and returns its path.
func schedScript(t *testing.T, h *moveHome, withFiles bool) string {
	t.Helper()
	dir := filepath.Join(h.Root, "script", "skills", "install-agent-director")
	b, err := os.ReadFile(h.S.Install)
	if err != nil {
		t.Fatal(err)
	}
	h.Write(filepath.Join(dir, "install.sh"), string(b), 0o755)
	if withFiles {
		if err := copyDir(h.S.ScheduleSrc, filepath.Join(dir, "schedule")); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "install.sh")
}

// schedRun runs the install the case set up (the pair, h.Flags, then args) as
// script, a file on disk, or, with piped, as `bash -s -- ...` reading it from a
// pipe as `curl ... | bash -s --` does.
func schedRun(t *testing.T, h *moveHome, script string, piped bool, args ...string) moveResult {
	t.Helper()
	return schedRunArgv(t, h, script, piped, schedArgv(h, args...))
}

// schedArgv is the argument list schedRun gives install.sh: the pair, h.Flags,
// then args.
func schedArgv(h *moveHome, args ...string) []string {
	argv := append([]string{"--binary", h.Pair.Main, "--admin-binary", h.Pair.Admin}, h.Flags...)
	return append(argv, args...)
}

// schedBareWord matches the words bash's printf %q leaves as they are.
var schedBareWord = regexp.MustCompile(`^[A-Za-z0-9_./:@+=,-]+$`)

// schedRerun is the command the warning gives to repeat the install of script
// (run in the directory cwd) with argv plus --schedule: `cd <cwd> && bash
// <script> <argv...>`, each word as bash quotes it. Every word the cases
// use is bare; a word that is not fails the case, so the expectation is never
// guessed.
func schedRerun(t *testing.T, cwd, script string, argv []string) string {
	t.Helper()
	words := append([]string{"bash", script}, argv...)
	for _, w := range append([]string{cwd}, words...) {
		if !schedBareWord.MatchString(w) {
			t.Fatalf("schedRerun cannot write the word %q without quoting", w)
		}
	}
	cd := "cd " + cwd + " && "
	for _, w := range argv {
		if w == "--schedule" {
			return cd + strings.Join(words, " ")
		}
	}
	return cd + strings.Join(append(words, "--schedule"), " ")
}

// schedRunArgv is schedRun with install.sh's whole argument list.
func schedRunArgv(t *testing.T, h *moveHome, script string, piped bool, argv []string) moveResult {
	t.Helper()
	cmd := h.command()
	if piped {
		f, err := os.Open(script)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Args = append([]string{cmd.Args[0], "-s", "--"}, argv...)
		cmd.Stdin = f
	} else {
		cmd.Args = append([]string{cmd.Args[0], script}, argv...)
	}
	return schedExec(t, cmd)
}

// schedExec runs cmd and returns its result.
func schedExec(t *testing.T, cmd *exec.Cmd) moveResult {
	t.Helper()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	res := moveResult{}
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		res.Code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Code = 128 + int(ws.Signal())
		}
	} else if err != nil {
		t.Fatalf("%v: %v", cmd.Args, err)
	}
	res.Stdout, res.Stderr = out.String(), errb.String()
	return res
}

// schedCronLines are the two crontab lines an install of h writes, for the
// tmux directory the case's PATH gives it and TMUX_TMPDIR tmuxTmp ("" is unset).
func schedCronLines(h *moveHome, tmuxTmp string) []string {
	return schedCronLinesHome(h, tmuxTmp, "")
}

// schedCronLinesHome is schedCronLines for an install that carries HOME home
// ("" is none: HOME is the passwd entry's).
func schedCronLinesHome(h *moveHome, tmuxTmp, home string) []string {
	env := schedCronPrefix(h, tmuxTmp, home)
	canon := ownedCanonical(h)
	return []string{
		"*/2 * * * * " + env + " " + canon + " find-missing >/dev/null # " + schedMark,
		"17 3 * * * " + env + " " + canon + " expire >/dev/null # " + schedMark,
	}
}

// schedCronBlock is what an install adds to the crontab: the cron file's own
// marked comment line (it is not a note, so it is installed), then the two lines.
func schedCronBlock(h *moveHome, tmuxTmp string) string { return schedCronBlockHome(h, tmuxTmp, "") }

// schedCronBlockHome is schedCronBlock for an install that carries HOME home.
func schedCronBlockHome(h *moveHome, tmuxTmp, home string) string {
	return "# " + schedMark + ": the find-missing and expire sweeps (install-agent-director skill, schedule/agent-director.cron)\n" +
		strings.Join(schedCronLinesHome(h, tmuxTmp, home), "\n") + "\n"
}

// schedUnitDir is where the user's systemd units go.
func schedUnitDir(h *moveHome) string { return filepath.Join(h.Home, ".config", "systemd", "user") }

// schedUnitsWritten reads the four unit files an install wrote; it fails the
// case when one is missing.
func schedUnitsWritten(t *testing.T, h *moveHome) map[string]string {
	t.Helper()
	return schedUnitsWrittenIn(t, schedUnitDir(h))
}

// schedUnitsWrittenIn is schedUnitsWritten for the unit directory dir.
func schedUnitsWrittenIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	units := map[string]string{}
	for _, f := range schedFiles[1:] {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("unit %s: %v", f, err)
		}
		units[f] = string(b)
	}
	return units
}

// schedSeedUnits writes the four marked unit files, as an earlier install did.
func schedSeedUnits(h *moveHome, body string) { schedSeedUnitsIn(h, schedUnitDir(h), body) }

// schedSeedUnitsIn is schedSeedUnits for the unit directory dir.
func schedSeedUnitsIn(h *moveHome, dir, body string) {
	for _, f := range schedFiles[1:] {
		h.Write(filepath.Join(dir, f), body, 0o644)
	}
}

// wantLines fails the case unless each of lines is a whole line of text, in order.
func wantLines(t *testing.T, name, text string, lines ...string) {
	t.Helper()
	have := strings.Split(text, "\n")
	i := 0
	for _, l := range lines {
		for i < len(have) && have[i] != l {
			i++
		}
		if i == len(have) {
			t.Errorf("%s lacks the line %q (in order after the one before it):\n%s", name, l, text)
			return
		}
		i++
	}
}

// before reports whether a comes before b in text, both being there.
func before(text, a, b string) bool {
	i, j := strings.Index(text, a), strings.Index(text, b)
	return i >= 0 && j >= 0 && i < j
}

// schedDone is install.sh's closing stdout line of a clean install.
const schedDone = "install.sh: done. Try: "

// TestScheduleWarning: an install with nothing scheduled ends with a loud
// warning on stderr after its "done" line, naming the lines to add; one whose
// entries are in place says it found them and does not warn. Without
// --schedule it changes nothing, and no outcome changes the exit status.
func TestScheduleWarning(t *testing.T) {
	t.Run("nothing-scheduled-the-whole-warning", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		script := schedScript(t, h, true)
		res := schedRun(t, h, script, false)
		if res.Code != 0 {
			t.Fatalf("exit = %d, want 0\n%s", res.Code, res.Stderr)
		}
		if !strings.Contains(res.Stdout, schedDone) || strings.Contains(res.Stdout, "WARNING") {
			t.Errorf("stdout wants the done line and no warning:\n%s", res.Stdout)
		}
		lines := schedCronLines(h, "")
		rerun := schedRerun(t, h.Cwd, script, schedArgv(h))
		want := strings.Join([]string{
			"",
			schedWarnHead,
			"  Until they run, a live row whose agent is gone (killed, crashed, rebooted) stays live, a relay row",
			"  left in check_permission stays there, a killed agent's running subagent records are not turned lost,",
			"  and finished rows are never expired. find-missing is what keeps the row states honest.",
			"  Schedule both now, as the agents' user (the user the agents run as, in their tmux environment). Run as",
			"  another user, as root or against another tmux server, a sweep can wrongly mark live rows missing or",
			"  delete rows whose agent still runs.",
			"  These crontab lines (crontab -e, as that user) run this install's binary:",
			"    " + lines[0],
			"    " + lines[1],
			"  They ship, with a systemd user timer and service for each sweep, as files in:",
			"    " + filepath.Dir(script) + "/schedule/",
			"    (agent-director.cron, agent-director-find-missing.service and .timer, agent-director-expire.service and .timer)",
			"  Copy them by hand (the lines above are the cron file's with its placeholders filled in: copy those; a unit",
			"  file's notes say how to fill its placeholders), or let the install script install them, as the user the",
			"  agents run as:",
			"    " + rerun,
			"  (--schedule uses cron when crontab works here and a cron daemon runs, else systemd user timers, else it installs nothing.)",
			"  Already scheduled another way (launchd, your own cron line or unit)? This check sees only the entries",
			"  --schedule adds, so it warns anyway: ignore this, or run --schedule and drop your own entries.",
			"",
		}, "\n")
		if !strings.HasSuffix(res.Stderr, want) {
			t.Errorf("stderr does not end with the warning:\n%s\nwant it to end with:\n%s", res.Stderr, want)
		}
	})

	// exit-6: the warning comes before the exit-6 line, which stays last.
	t.Run("an-MCP-failure-keeps-exit-6-and-its-last-line", func(t *testing.T) {
		t.Parallel()
		_, res := ownedInstall(t, ownedSets(t)[0], "none-found-many-others", true, map[string]string{"CLAUDE_STANDIN_ADD_FAIL": "1"})
		if res.Code != 6 || res.Last() != ownedFailLast {
			t.Errorf("exit = %d, last stderr line = %q, want 6 and %q\n%s", res.Code, res.Last(), ownedFailLast, res.Stderr)
		}
		if !before(res.Stderr, "claude mcp add agent-director -- ", schedWarnHead) || !before(res.Stderr, schedWarnHead, ownedFailLast) {
			t.Errorf("stderr wants the MCP advice, then the schedule warning, then the exit-6 line:\n%s", res.Stderr)
		}
	})

	rows := []struct {
		name      string
		crontab   string // the user's crontab; "" none
		units     bool   // this install's four unit files in place
		warn      bool
		foundLine string
	}{
		{name: "an-empty-crontab", warn: true},
		{name: "foreign-lines-only", crontab: "MAILTO=me\n0 * * * * /usr/bin/other\n", warn: true},
		{name: "only-find-missing-is-scheduled", crontab: "*/2 * * * * /x/agent-director find-missing >/dev/null # " + schedMark + "\n", warn: true},
		{name: "a-comment-naming-the-marker-is-not-an-entry", crontab: "# " + schedMark + " find-missing expire\n", warn: true},
		{name: "both-in-cron", crontab: "*/2 * * * * /x/agent-director find-missing >/dev/null # " + schedMark + "\n17 3 * * * /x/agent-director expire >/dev/null # " + schedMark + "\n", foundLine: schedFoundCron},
		{name: "the-four-units", units: true, foundLine: schedFoundSystemd},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron(row.crontab)
			stub.Systemctl(0)
			if row.units {
				schedSeedUnits(h, "# "+schedMark+"\n")
			}
			res := schedRun(t, h, schedScript(t, h, true), false)
			if res.Code != 0 {
				t.Fatalf("exit = %d, want 0\n%s", res.Code, res.Stderr)
			}
			if got := strings.Contains(res.Stderr, schedWarnHead); got != row.warn {
				t.Errorf("warning on stderr = %v, want %v:\n%s", got, row.warn, res.Stderr)
			}
			if row.foundLine != "" {
				wantLines(t, "stdout", res.Stdout, row.foundLine)
			}
			if stub.CronWrites() != 0 || slicesContainEnable(stub.SystemctlCalls()) {
				t.Errorf("an install without --schedule changed the schedule: crontab calls %v, systemctl calls %v", stub.CronCalls(), stub.SystemctlCalls())
			}
		})
	}
}

// slicesContainEnable reports whether a systemctl call enabled something.
func slicesContainEnable(calls []string) bool {
	for _, c := range calls {
		if strings.Contains(c, " enable") || strings.Contains(c, "restart") {
			return true
		}
	}
	return false
}

// schedScheduleOut is the stdout line --schedule prints for a fresh cron install.
const (
	schedCronAdded     = "  schedule: cron: find-missing every 2 minutes and expire daily at 03:17 added to this user's crontab, each line tagged # " + schedMark + ". The cron daemon must be running."
	schedCronUnchanged = "  schedule: cron: this user's crontab already holds these entries (find-missing every 2 minutes, expire daily at 03:17); nothing changed. The cron daemon must be running."
)

// TestScheduleCron: --schedule with a working crontab adds the two tagged
// lines for the install's binary; every other line of the crontab stays as it
// was, a re-run leaves one copy of each, and an upgrade re-points the old ones.
func TestScheduleCron(t *testing.T) {
	foreign := "MAILTO=me\n# my own job\n0 * * * * /usr/bin/other\n"
	stale := "*/2 * * * * PATH=/old /old/bin/agent-director find-missing >/dev/null # " + schedMark + "\n" +
		"17 3 * * * PATH=/old /old/bin/agent-director expire >/dev/null # " + schedMark + "\n"
	rows := []struct {
		name    string
		crontab string
		tmpdir  string
		want    func(h *moveHome) string // the crontab after
		out     string
	}{
		{name: "no-crontab-yet", out: schedCronAdded,
			want: func(h *moveHome) string { return schedCronBlock(h, "") }},
		{name: "foreign-lines-are-kept-in-place", crontab: foreign, out: schedCronAdded,
			want: func(h *moveHome) string { return foreign + schedCronBlock(h, "") }},
		{name: "an-upgrade-re-points-the-old-lines", crontab: "MAILTO=me\n" + stale + "0 * * * * /usr/bin/other\n", out: schedCronAdded,
			want: func(h *moveHome) string {
				return "MAILTO=me\n0 * * * * /usr/bin/other\n" + schedCronBlock(h, "")
			}},
		{name: "tmux-tmpdir-reaches-the-lines", tmpdir: "/run/ad-tmux", out: schedCronAdded,
			want: func(h *moveHome) string { return schedCronBlock(h, "/run/ad-tmux") }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron(row.crontab)
			stub.Systemctl(0)
			if row.tmpdir != "" {
				h.Env["TMUX_TMPDIR"] = row.tmpdir
			}
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Fatalf("exit = %d, want 0\n%s", res.Code, res.Stderr)
			}
			if got, _ := stub.Crontab(); got != row.want(h) {
				t.Errorf("crontab =\n%s\nwant\n%s", got, row.want(h))
			}
			wantLines(t, "stdout", res.Stdout, row.out, "  admin   : "+filepath.Join(h.New, "admin", "agent-director-admin")+" (operator tool, not on PATH; run it only with a human's explicit approval for that run)")
			if strings.Contains(res.Stderr, "WARNING") {
				t.Errorf("an install that scheduled warns:\n%s", res.Stderr)
			}
			if _, err := os.Stat(schedUnitDir(h)); err == nil {
				t.Errorf("%s exists: a cron install also wrote systemd units", schedUnitDir(h))
			}

			// A re-run adds nothing: one copy of each line, no write, and says so.
			again := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if got, _ := stub.Crontab(); got != row.want(h) || stub.CronWrites() != 1 {
				t.Errorf("after a re-run: crontab =\n%s\nwrites = %d; want the same crontab and 1 write in all", got, stub.CronWrites())
			}
			wantLines(t, "re-run stdout", again.Stdout, schedCronUnchanged)
			if again.Code != 0 || strings.Contains(again.Stderr, "WARNING") {
				t.Errorf("re-run: exit %d, stderr:\n%s", again.Code, again.Stderr)
			}
		})
	}

	// The cron install makes a copy of this install's systemd units redundant: it
	// removes the marked ones and no other file.
	t.Run("an-earlier-installs-units-are-removed", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Systemctl(0)
		schedSeedUnits(h, "# "+schedMark+"\n[Unit]\n")
		keep := filepath.Join(schedUnitDir(h), "other.service")
		h.Write(keep, "[Unit]\n", 0o644)
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		for _, f := range schedFiles[1:] {
			if _, err := os.Stat(filepath.Join(schedUnitDir(h), f)); err == nil {
				t.Errorf("%s is still there after a cron install", f)
			}
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("an unrelated unit was removed: %v", err)
		}
	})
}

// schedBusyboxNoCrontab is what busybox's `crontab -l` says (and exits 1) for a
// user with no crontab yet: not "no crontab for <user>".
const schedBusyboxNoCrontab = "crontab: can't open 'tester': No such file or directory"

// TestScheduleBusyboxCrontab: busybox's "No such file or directory" is "no
// crontab yet", as "no crontab for <user>" is: --schedule adds its lines and an
// install without it warns of nothing scheduled, not of an unreadable crontab.
func TestScheduleBusyboxCrontab(t *testing.T) {
	t.Run("--schedule-adds-the-lines", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		h.Env["CRON_STUB_NOFILE_MSG"] = schedBusyboxNoCrontab
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, schedCronAdded)
		if got, _ := stub.Crontab(); got != schedCronBlock(h, "") {
			t.Errorf("crontab =\n%s\nwant\n%s", got, schedCronBlock(h, ""))
		}
	})

	t.Run("a-plain-install-warns-of-nothing-scheduled-not-of-a-failed-read", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		h.Env["CRON_STUB_NOFILE_MSG"] = schedBusyboxNoCrontab
		res := schedRun(t, h, schedScript(t, h, true), false)
		if res.Code != 0 || !strings.Contains(res.Stderr, schedWarnHead) {
			t.Fatalf("exit = %d, want 0 and the warning:\n%s", res.Code, res.Stderr)
		}
		if strings.Contains(res.Stderr, "crontab -l failed") || strings.Contains(res.Stderr, "No such file") {
			t.Errorf("the warning treats a missing crontab as a failed read:\n%s", res.Stderr)
		}
		wantLines(t, "stderr", res.Stderr, "  These crontab lines (crontab -e, as that user) run this install's binary:")
	})
}

// TestScheduleSystemd: with no usable crontab, --schedule writes the four
// units with the install's binary and enables the two timers; a failure on the
// way leaves no unit behind.
func TestScheduleSystemd(t *testing.T) {
	unitsWant := func(t *testing.T, h *moveHome, tmpdir string) {
		t.Helper()
		schedUnitsWant(t, h, schedUnitEnv(h, tmpdir, ""))
	}

	t.Run("no-crontab-installs-and-enables-the-timers", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Systemctl(0)
		h.Env["TMUX_TMPDIR"] = "/run/ad-tmux"
		script := schedScript(t, h, true)
		res := schedRun(t, h, script, false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		unitsWant(t, h, "/run/ad-tmux")
		wantLines(t, "stdout", res.Stdout,
			"  schedule: systemd: user timers agent-director-find-missing.timer (every 2 minutes) and agent-director-expire.timer (daily at 03:17) installed in "+schedUnitDir(h)+" and enabled.",
			"  schedule: a user timer runs only while this user's systemd manager does: on a host where the user is not always logged in, run loginctl enable-linger "+currentUser(t)+" once.")
		timers := "agent-director-find-missing.timer agent-director-expire.timer"
		wantLines(t, "systemctl calls", strings.Join(stub.SystemctlCalls(), "\n"), "--user show-environment", "--user daemon-reload", "--user enable "+timers, "--user restart "+timers)

		// A re-run writes the same files and enables again.
		again := schedRun(t, h, script, false, "--schedule")
		wantLines(t, "re-run stdout", again.Stdout, "  schedule: systemd: the user timers already held these units; enabled again, nothing changed ("+schedUnitDir(h)+")")
		unitsWant(t, h, "/run/ad-tmux")
		if strings.Contains(again.Stderr, "WARNING") {
			t.Errorf("re-run warns:\n%s", again.Stderr)
		}
	})

	t.Run("the-shells-XDG_CONFIG_HOME-names-the-unit-directory-when-the-manager-does-not-say", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		schedStub{h}.Systemctl(0)
		xdg := filepath.Join(h.Root, "xdg")
		h.Env["XDG_CONFIG_HOME"] = xdg
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		for _, f := range schedFiles[1:] {
			if _, err := os.Stat(filepath.Join(xdg, "systemd", "user", f)); err != nil {
				t.Errorf("%v", err)
			}
		}
	})

	// The unit directory is the one the user manager reads, as its
	// `systemctl --user show-environment` tells: an absolute XDG_CONFIG_HOME's
	// systemd/user, else an absolute HOME's .config/systemd/user, else this
	// shell's own rule. The manager's environment can differ from this shell's.
	mgrRows := []struct {
		name  string
		shell string                     // XDG_CONFIG_HOME in the install's environment; "" unset
		mgr   func(h *moveHome) []string // the manager's environment
		dirOf func(h *moveHome) string   // the unit directory wanted
		decoy func(h *moveHome) []string // directories that must stay empty
	}{
		{name: "the-managers-XDG_CONFIG_HOME-wins-over-this-shells-rule",
			shell: "shell-xdg",
			mgr:   func(h *moveHome) []string { return []string{"XDG_CONFIG_HOME=" + filepath.Join(h.Root, "mgr-xdg")} },
			dirOf: func(h *moveHome) string { return filepath.Join(h.Root, "mgr-xdg", "systemd", "user") },
			decoy: func(h *moveHome) []string {
				return []string{schedUnitDir(h), filepath.Join(h.Root, "shell-xdg", "systemd", "user")}
			}},
		{name: "the-managers-HOME-names-the-directory",
			mgr: func(h *moveHome) []string {
				return []string{"PATH=/usr/bin", "HOME=" + filepath.Join(h.Root, "mgr-home")}
			},
			dirOf: func(h *moveHome) string { return filepath.Join(h.Root, "mgr-home", ".config", "systemd", "user") },
			decoy: func(h *moveHome) []string { return []string{schedUnitDir(h)} }},
		{name: "the-managers-XDG_CONFIG_HOME-beats-its-HOME",
			mgr: func(h *moveHome) []string {
				return []string{"HOME=" + filepath.Join(h.Root, "mgr-home"), "XDG_CONFIG_HOME=" + filepath.Join(h.Root, "mgr-xdg")}
			},
			dirOf: func(h *moveHome) string { return filepath.Join(h.Root, "mgr-xdg", "systemd", "user") },
			decoy: func(h *moveHome) []string {
				return []string{schedUnitDir(h), filepath.Join(h.Root, "mgr-home", ".config", "systemd", "user")}
			}},
		{name: "a-relative-manager-XDG_CONFIG_HOME-is-ignored",
			mgr: func(h *moveHome) []string {
				return []string{"XDG_CONFIG_HOME=relative/dir", "HOME=" + filepath.Join(h.Root, "mgr-home")}
			},
			dirOf: func(h *moveHome) string { return filepath.Join(h.Root, "mgr-home", ".config", "systemd", "user") },
			decoy: func(h *moveHome) []string { return []string{schedUnitDir(h)} }},
		{name: "a-manager-saying-neither-leaves-this-shells-rule",
			shell: "shell-xdg",
			mgr:   func(h *moveHome) []string { return []string{"PATH=/usr/bin", "LANG=C"} },
			dirOf: func(h *moveHome) string { return filepath.Join(h.Root, "shell-xdg", "systemd", "user") },
			decoy: func(h *moveHome) []string { return []string{schedUnitDir(h)} }},
		{name: "a-manager-saying-neither-and-no-shell-XDG-is-the-shell-HOME",
			mgr:   func(h *moveHome) []string { return []string{"PATH=/usr/bin"} },
			dirOf: schedUnitDir},
	}
	for _, row := range mgrRows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Systemctl(0)
			stub.ManagerEnv(row.mgr(h)...)
			if row.shell != "" {
				h.Env["XDG_CONFIG_HOME"] = filepath.Join(h.Root, row.shell)
			}
			dir := row.dirOf(h)
			script := schedScript(t, h, true)
			res := schedRun(t, h, script, false, "--schedule")
			if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
				t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
			}
			schedUnitsWantIn(t, h, dir, schedUnitEnv(h, "", ""))
			wantLines(t, "stdout", res.Stdout,
				"  schedule: systemd: user timers agent-director-find-missing.timer (every 2 minutes) and agent-director-expire.timer (daily at 03:17) installed in "+dir+" and enabled.")
			for _, tm := range []string{"agent-director-find-missing.timer", "agent-director-expire.timer"} {
				if _, err := os.Lstat(filepath.Join(dir, "timers.target.wants", tm)); err != nil {
					t.Errorf("the timer is not enabled where the manager reads it: %v", err)
				}
			}
			if row.decoy != nil {
				for _, d := range row.decoy(h) {
					if _, err := os.Stat(d); err == nil {
						t.Errorf("%s exists: units went to a directory the manager does not read", d)
					}
				}
			}
			if n := strings.Count(strings.Join(stub.SystemctlCalls(), "\n"), "--user show-environment"); n != 1 {
				t.Errorf("the manager's environment was read %d times, want once: %v", n, stub.SystemctlCalls())
			}

			// A re-run finds them in the same directory.
			again := schedRun(t, h, script, false, "--schedule")
			wantLines(t, "re-run stdout", again.Stdout, "  schedule: systemd: the user timers already held these units; enabled again, nothing changed ("+dir+")")
		})
	}

	// Without --schedule the units count as in place only where the manager reads them.
	t.Run("units-in-the-managers-directory-are-found-and-ones-elsewhere-are-not", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Systemctl(0)
		mgrHome := filepath.Join(h.Root, "mgr-home")
		stub.ManagerEnv("HOME=" + mgrHome)
		script := schedScript(t, h, true)
		schedSeedUnitsIn(h, schedUnitDir(h), "# "+schedMark+"\n")
		if res := schedRun(t, h, script, false); res.Code != 0 || !strings.Contains(res.Stderr, schedWarnHead) {
			t.Errorf("units only in this shell's directory: exit %d, want the warning:\n%s", res.Code, res.Stderr)
		}
		schedSeedUnitsIn(h, filepath.Join(mgrHome, ".config", "systemd", "user"), "# "+schedMark+"\n")
		res := schedRun(t, h, script, false)
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, schedFoundSystemd)
	})

	t.Run("the-crontab-failing-falls-through-to-systemd", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		h.Env["CRON_STUB_WRITE_FAIL"] = "crontab: installing new crontab failed"
		stub.Systemctl(0)
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		unitsWant(t, h, "")
		if text, ok := stub.Crontab(); ok {
			t.Errorf("a refused crontab was written: %q", text)
		}
	})

	// Neither works, or the timers cannot be enabled: nothing is left installed,
	// the exit status is 0, and the warning gives the reason.
	rows := []struct {
		name   string
		setup  func(h *moveHome, stub schedStub)
		reason string
	}{
		{name: "neither-crontab-nor-systemctl", setup: func(h *moveHome, stub schedStub) {},
			reason: "cron: crontab is not installed; systemd: systemctl is not installed"},
		{name: "the-user-manager-is-not-reachable", setup: func(h *moveHome, stub schedStub) { stub.Systemctl(1) },
			reason: "cron: crontab is not installed; systemd: the systemd user manager is not reachable (systemctl --user show-environment failed)"},
		{name: "crontab-list-fails-and-no-systemd", setup: func(h *moveHome, stub schedStub) {
			stub.Cron("")
			h.Env["CRON_STUB_LIST_FAIL"] = "crontab: cannot open the spool"
		}, reason: "cron: crontab -l failed (exit 1): crontab: cannot open the spool; systemd: systemctl is not installed"},
		{name: "crontab-refuses-the-new-crontab", setup: func(h *moveHome, stub schedStub) {
			stub.Cron("")
			h.Env["CRON_STUB_WRITE_FAIL"] = "crontab: bad minute"
		}, reason: "cron: crontab - refused the new crontab (exit 1): crontab: bad minute; systemd: systemctl is not installed"},
		{name: "the-timers-cannot-be-enabled", setup: func(h *moveHome, stub schedStub) {
			stub.Systemctl(0)
			h.Env["SYSCTL_STUB_FAIL"] = "enable"
		}, reason: "cron: crontab is not installed; systemd: systemctl --user could not enable the timers"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			row.setup(h, schedStub{h})
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Errorf("exit = %d, want 0 (a failed schedule does not fail the install)\n%s", res.Code, res.Stderr)
			}
			wantLines(t, "stderr", res.Stderr, schedWarnHead, "  --schedule was asked for and installed nothing: "+row.reason)
			if strings.Contains(res.Stdout, "  schedule: cron") || strings.Contains(res.Stdout, "  schedule: systemd") {
				t.Errorf("stdout claims a schedule:\n%s", res.Stdout)
			}
			for _, f := range schedFiles[1:] {
				if _, err := os.Stat(filepath.Join(schedUnitDir(h), f)); err == nil {
					t.Errorf("%s was left behind", f)
				}
			}
		})
	}
}

// currentUser is the name `id -un` gives the tests.
func currentUser(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestScheduleRefusesAnotherUsersHome: --schedule run as a user other than the
// one that owns HOME installs nothing, since the sweeps must run as the agents'
// user, and says why.
func TestScheduleRefusesAnotherUsersHome(t *testing.T) {
	t.Parallel()
	h := newMoveHome(t)
	stub := schedStub{h}
	stub.Cron("")
	stub.Systemctl(0)
	// An id that names another user than the one that owns HOME.
	h.WriteTool("id", `case "$1" in -u) echo $(( $(`+moveQuote(h.RealTool("id"))+` -u) + 1 )) ;; -un) echo mallory ;; *) exec `+moveQuote(h.RealTool("id"))+` "$@" ;; esac`)
	res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
	if res.Code != 0 {
		t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
	}
	wantLines(t, "stderr", res.Stderr, schedWarnHead,
		fmt.Sprintf("  --schedule was asked for and installed nothing: this run is user mallory but %s belongs to another user (uid %d), and the sweeps must run as the agents' user: run install.sh --schedule as that user", h.Home, os.Getuid()))
	if text, ok := stub.Crontab(); ok || stub.CronWrites() != 0 {
		t.Errorf("a crontab was written for another user's HOME: %q", text)
	}
	if _, err := os.Stat(schedUnitDir(h)); err == nil {
		t.Errorf("units were written for another user's HOME")
	}
}

// schedURL is the raw URL the schedule file f is fetched from at ref.
func schedURL(ref, f string) string {
	return schedRawBase + ref + "/skills/install-agent-director/schedule/" + f
}

// schedFetchedDir is where a fetch keeps the five files.
func schedFetchedDir(h *moveHome) string { return filepath.Join(h.New, "schedule") }

// schedNothingKept fails the case when a failed fetch left anything in the
// config directory: no schedule directory and no temp directory.
func schedNothingKept(t *testing.T, h *moveHome) {
	t.Helper()
	entries, _ := os.ReadDir(h.New)
	for _, e := range entries {
		if e.Name() == "schedule" || strings.HasPrefix(e.Name(), ".schedule.") {
			t.Errorf("%s is left in %s", e.Name(), h.New)
		}
	}
}

// TestSchedulePiped: an install.sh read from a pipe has no schedule/ beside it,
// so --schedule fetches the five files from the repository at the binaries'
// commit stamp and keeps them in ~/.config/agent-director/schedule/; the fetch
// is all or nothing and a failed one never changes the exit status.
func TestSchedulePiped(t *testing.T) {
	var urls []string
	for _, f := range schedFiles {
		urls = append(urls, schedURL(moveCommit, f))
	}
	fetched := "  schedule: the schedule files are not beside this script; fetched them from " + schedRawBase + moveCommit + "/skills/install-agent-director/schedule/ to "

	t.Run("--schedule-fetches-keeps-and-installs", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		script := schedScript(t, h, false)
		res := schedRun(t, h, script, true, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		if got := stub.CurlURLs(); strings.Join(got, "\n") != strings.Join(urls, "\n") {
			t.Errorf("curl was asked for\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(urls, "\n"))
		}
		for _, f := range schedFiles {
			want, _ := os.ReadFile(filepath.Join(h.S.ScheduleSrc, f))
			if got, err := os.ReadFile(filepath.Join(schedFetchedDir(h), f)); err != nil || string(got) != string(want) {
				t.Errorf("kept %s = %q (%v), want the repository's file", f, got, err)
			}
		}
		wantLines(t, "stdout", res.Stdout, fetched+schedFetchedDir(h), schedCronAdded)
		if got, _ := stub.Crontab(); got != schedCronBlock(h, "") {
			t.Errorf("crontab =\n%s\nwant\n%s", got, schedCronBlock(h, ""))
		}
	})

	t.Run("without---schedule-the-warning-reads-the-kept-copy", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		res := schedRun(t, h, schedScript(t, h, false), true)
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		lines := schedCronLines(h, "")
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  These crontab lines (crontab -e, as that user) run this install's binary:", "    "+lines[0], "    "+lines[1],
			"    "+schedFetchedDir(h)+"/",
			"  This script was not run from a file, so it fetched them from the repository at "+moveCommit,
			"  (skills/install-agent-director/schedule/ at "+schedRawBase+moveCommit+"/) and kept the copy there.",
			"    run the same install command again with --schedule added")
		if stub.CronWrites() != 0 {
			t.Errorf("an install without --schedule wrote the crontab")
		}
		for _, f := range schedFiles {
			if _, err := os.Stat(filepath.Join(schedFetchedDir(h), f)); err != nil {
				t.Errorf("%v", err)
			}
		}
	})

	t.Run("a-script-file-with-no-schedule-directory-fetches-too", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		res := schedRun(t, h, schedScript(t, h, false), false, "--schedule")
		if res.Code != 0 || len(stub.CurlURLs()) != len(urls) {
			t.Errorf("exit = %d, curl asked for %v; want 0 and the five files", res.Code, stub.CurlURLs())
		}
		wantLines(t, "stdout", res.Stdout, fetched+schedFetchedDir(h), schedCronAdded)
	})

	t.Run("files-beside-the-script-fetch-nothing", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || len(stub.CurlURLs()) != 0 {
			t.Errorf("exit = %d, curl asked for %v; want 0 and nothing", res.Code, stub.CurlURLs())
		}
		schedNothingKept(t, h)
	})

	t.Run("already-scheduled-fetches-nothing", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(schedCronBlock(h, ""))
		stub.Curl()
		res := schedRun(t, h, schedScript(t, h, false), true)
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") || len(stub.CurlURLs()) != 0 {
			t.Errorf("exit = %d, curl asked for %v, stderr:\n%s\nwant 0, nothing fetched and no warning", res.Code, stub.CurlURLs(), res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, schedFoundCron)
		schedNothingKept(t, h)
	})

	// A failure ends the fetch at once, except a 404 at the commit's ref, which
	// is retried once at main: so a 404 that main answers too reads "<first>; and
	// at main: <second>", and the fallback link points at main.
	notFound := func(ref, f string) string { return "GET " + schedURL(ref, f) + " gave HTTP 404" }
	failures := []struct {
		name  string
		env   map[string]string
		noCmd bool   // no curl at all
		why   string // SCHED_FETCH_WHY
		ref   string // the ref the fallback link names
	}{
		{name: "a-404-writes-nothing", env: map[string]string{"CURL_STUB_404": "agent-director-find-missing.timer"},
			why: notFound(moveCommit, "agent-director-find-missing.timer") + "; and at main: " + notFound("main", "agent-director-find-missing.timer"), ref: "main"},
		{name: "a-404-at-the-commit-and-at-main-writes-nothing", env: map[string]string{"CURL_STUB_404_REFS": moveCommit + " main"},
			why: notFound(moveCommit, "agent-director.cron") + "; and at main: " + notFound("main", "agent-director.cron"), ref: "main"},
		{name: "a-404-at-the-commit-and-a-page-at-main-writes-nothing", env: map[string]string{"CURL_STUB_404_REFS": moveCommit, "CURL_STUB_HTML": "agent-director.cron"},
			why: notFound(moveCommit, "agent-director.cron") + "; and at main: GET " + schedURL("main", "agent-director.cron") + " did not return a schedule file", ref: "main"},
		{name: "a-page-that-is-not-a-schedule-file-writes-nothing", env: map[string]string{"CURL_STUB_HTML": "agent-director.cron"},
			why: "GET " + schedURL(moveCommit, "agent-director.cron") + " did not return a schedule file", ref: moveCommit},
		{name: "no-curl", noCmd: true, why: "curl is not installed", ref: moveCommit},
	}
	for _, row := range failures {
		for _, sched := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/schedule=%v", row.name, sched), func(t *testing.T) {
				t.Parallel()
				h := newMoveHome(t)
				stub := schedStub{h}
				stub.Cron("")
				if !row.noCmd {
					stub.Curl()
				}
				for k, v := range row.env {
					h.Env[k] = v
				}
				var args []string
				if sched {
					args = append(args, "--schedule")
				}
				res := schedRun(t, h, schedScript(t, h, false), true, args...)
				if res.Code != 0 {
					t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
				}
				wantLines(t, "stderr", res.Stderr, schedWarnHead,
					"  This script has no schedule files beside it and could not fetch them: "+row.why+".",
					"  The schedule files (a crontab file and a systemd user timer and service for each sweep) are in the",
					"  agent-director repository, in skills/install-agent-director/schedule/",
					"  (https://github.com/gabemahoney/agent-director/tree/"+row.ref+"/skills/install-agent-director/schedule),")
				if sched {
					wantLines(t, "stderr", res.Stderr, "  --schedule was asked for and installed nothing: the schedule files are not beside this script and could not be fetched: "+row.why)
				}
				if strings.Contains(res.Stdout, "fetched them") || strings.Contains(res.Stderr, "These crontab lines") {
					t.Errorf("the install claims files it does not have:\n%s\n%s", res.Stdout, res.Stderr)
				}
				schedNothingKept(t, h)
				if text, ok := stub.Crontab(); ok || stub.CronWrites() != 0 {
					t.Errorf("a crontab was written without the files: %q", text)
				}
			})
		}
	}
}

// schedShortPair is a pair of the current binaries stamped with a commit that
// is not a full 40-hex id, built once.
var schedShortPair struct {
	once sync.Once
	pair movePair
	err  error
}

func schedShortStamp(t *testing.T, s *moveShared) movePair {
	t.Helper()
	schedShortPair.once.Do(func() {
		flags := fmt.Sprintf("-X %s.Version=%s -X %s.Commit=abc1234", moveVersPkg, moveVersion, moveVersPkg)
		for _, name := range []string{"agent-director", "agent-director-admin"} {
			out := filepath.Join(s.Root, "short-stamp", name)
			cmd := exec.Command("go", "build", "-ldflags", flags, "-o", out, "./cmd/"+name)
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				schedShortPair.err = fmt.Errorf("go build %s: %v\n%s", name, err, b)
				return
			}
			if name == "agent-director" {
				schedShortPair.pair.Main = out
			} else {
				schedShortPair.pair.Admin = out
			}
		}
	})
	if schedShortPair.err != nil {
		t.Fatal(schedShortPair.err)
	}
	return schedShortPair.pair
}

// TestScheduleFetchRef: the files are fetched at the release tag with
// --from-release, and, from local binaries, at their commit stamp when that is a
// full commit id; any other stamp gives no reliable ref, so nothing is fetched.
func TestScheduleFetchRef(t *testing.T) {
	t.Run("--from-release-uses-the-tag", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		// --from-release needs curl on PATH as well as the override.
		h.Write(filepath.Join(h.Tools, "curl"), "#!/bin/bash\nexec "+moveQuote(filepath.Join(h.Tools, "curl-stub"))+` "$@"`+"\n", 0o755)
		res := schedRunArgv(t, h, schedScript(t, h, false), true, append([]string{"--from-release", "v9.9.9"}, append(h.Flags, "--schedule")...))
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		var got []string
		for _, u := range stub.CurlURLs() {
			if strings.HasPrefix(u, schedRawBase) {
				got = append(got, u)
			}
		}
		var want []string
		for _, f := range schedFiles {
			want = append(want, schedURL("v9.9.9", f))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("schedule files fetched from\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		wantLines(t, "stdout", res.Stdout, "  schedule: the schedule files are not beside this script; fetched them from "+schedRawBase+"v9.9.9/skills/install-agent-director/schedule/ to "+schedFetchedDir(h), schedCronAdded)
	})

	// A release tag older than the schedule/ directory answers 404: the files are
	// then fetched once more at main, and the fetch line and the warning say so.
	fromRelease := func(t *testing.T, env map[string]string, args ...string) (*moveHome, schedStub, moveResult) {
		t.Helper()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		for k, v := range env {
			h.Env[k] = v
		}
		h.Write(filepath.Join(h.Tools, "curl"), "#!/bin/bash\nexec "+moveQuote(filepath.Join(h.Tools, "curl-stub"))+` "$@"`+"\n", 0o755)
		res := schedRunArgv(t, h, schedScript(t, h, false), true, append([]string{"--from-release", "v9.9.9"}, append(h.Flags, args...)...))
		return h, stub, res
	}
	schedGets := func(stub schedStub) []string {
		var got []string
		for _, u := range stub.CurlURLs() {
			if strings.HasPrefix(u, schedRawBase) {
				got = append(got, u)
			}
		}
		return got
	}
	tagNote := "v9.9.9 has no schedule files (GET " + schedURL("v9.9.9", "agent-director.cron") + " gave HTTP 404), so they come from main, which may be newer than this install"

	t.Run("--from-release-a-tag-404-retries-at-main-and-installs", func(t *testing.T) {
		t.Parallel()
		h, stub, res := fromRelease(t, map[string]string{"CURL_STUB_404_REFS": "v9.9.9"}, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		want := []string{schedURL("v9.9.9", "agent-director.cron")}
		for _, f := range schedFiles {
			want = append(want, schedURL("main", f))
		}
		if got := schedGets(stub); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("schedule files fetched from\n%s\nwant the tag's first file, then all five at main:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		wantLines(t, "stdout", res.Stdout,
			"  schedule: the schedule files are not beside this script; fetched them from "+schedRawBase+"main/skills/install-agent-director/schedule/ to "+schedFetchedDir(h)+" ("+tagNote+")",
			schedCronAdded)
		for _, f := range schedFiles {
			want, _ := os.ReadFile(filepath.Join(h.S.ScheduleSrc, f))
			if got, err := os.ReadFile(filepath.Join(schedFetchedDir(h), f)); err != nil || string(got) != string(want) {
				t.Errorf("kept %s = %q (%v), want the repository's file", f, got, err)
			}
		}
		if got, _ := stub.Crontab(); got != schedCronBlock(h, "") {
			t.Errorf("crontab =\n%s\nwant\n%s", got, schedCronBlock(h, ""))
		}
	})

	t.Run("--from-release-a-tag-404-without---schedule-the-warning-names-main", func(t *testing.T) {
		t.Parallel()
		h, stub, res := fromRelease(t, map[string]string{"CURL_STUB_404_REFS": "v9.9.9"})
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"    "+schedFetchedDir(h)+"/",
			"  This script was not run from a file, so it fetched them from the repository at main",
			"  (skills/install-agent-director/schedule/ at "+schedRawBase+"main/) and kept the copy there. ("+tagNote+")")
		if stub.CronWrites() != 0 {
			t.Errorf("an install without --schedule wrote the crontab")
		}
	})

	t.Run("--from-release-a-tag-404-and-a-main-404-says-both", func(t *testing.T) {
		t.Parallel()
		h, stub, res := fromRelease(t, map[string]string{"CURL_STUB_404_REFS": "v9.9.9 main"}, "--schedule")
		if res.Code != 0 {
			t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
		}
		why := "GET " + schedURL("v9.9.9", "agent-director.cron") + " gave HTTP 404; and at main: GET " + schedURL("main", "agent-director.cron") + " gave HTTP 404"
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  This script has no schedule files beside it and could not fetch them: "+why+".",
			"  (https://github.com/gabemahoney/agent-director/tree/main/skills/install-agent-director/schedule),")
		wantLines(t, "stderr", res.Stderr, "  --schedule was asked for and installed nothing: the schedule files are not beside this script and could not be fetched: "+why)
		if got := schedGets(stub); len(got) != 2 {
			t.Errorf("schedule files asked for %v, want one try at the tag and one at main", got)
		}
		schedNothingKept(t, h)
		if text, ok := stub.Crontab(); ok {
			t.Errorf("a crontab was written without the files: %q", text)
		}
	})

	t.Run("a-short-commit-stamp-fetches-nothing", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		h.Pair = schedShortStamp(t, h.S)
		stub := schedStub{h}
		stub.Cron("")
		stub.Curl()
		res := schedRun(t, h, schedScript(t, h, false), true, "--schedule")
		if res.Code != 0 || len(stub.CurlURLs()) != 0 {
			t.Errorf("exit = %d, curl asked for %v; want 0 and nothing\n%s", res.Code, stub.CurlURLs(), res.Stderr)
		}
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  This script has no schedule files beside it and could not fetch them: the installed binaries' commit stamp (abc1234) is not a full commit id, so there is no reliable repository ref to fetch the files at.")
		schedNothingKept(t, h)
	})
}
