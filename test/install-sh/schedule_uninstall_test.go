package installsh_test

// uninstall.sh removes the schedule install.sh --schedule added (b.wur): the
// crontab lines and the systemd units that carry the marker, and the copy of the
// schedule files a piped install kept. It touches nothing else, and with none
// present it is a silent no-op. The helpers are schedule_test.go's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schedUninstall runs uninstall.sh in the case's environment.
func schedUninstall(t *testing.T, h *moveHome) moveResult {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "uninstall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := h.command()
	cmd.Args = []string{cmd.Args[0], script}
	return schedExec(t, cmd)
}

// schedUninstalled fails the case unless uninstall.sh ended 0: what it reports
// about the schedule, even a failure, never changes its exit status.
func schedUninstalled(t *testing.T, res moveResult) {
	t.Helper()
	if res.Code != 0 {
		t.Fatalf("uninstall.sh exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
}

// schedCronRemoved counts the two jobs; the block's marked header comment goes
// with them and is not a line it reports.
const schedCronRemoved = "uninstall.sh: removed 2 schedule line(s) tagged # " + schedMark + " from the crontab"

func TestUninstallScheduleCron(t *testing.T) {
	foreign := "MAILTO=me\n# my own job\n0 * * * * /usr/bin/other\n"

	rows := []struct {
		name    string
		crontab func(h *moveHome) string
		after   string // the crontab after; "" none left
	}{
		{name: "only-the-tagged-lines-go", crontab: func(h *moveHome) string { return foreign + schedCronBlock(h, "") }, after: foreign},
		{name: "foreign-lines-between-ours-stay-in-order", crontab: func(h *moveHome) string {
			b := strings.SplitAfter(schedCronBlock(h, ""), "\n")
			return b[0] + "MAILTO=me\n" + b[1] + "0 * * * * /usr/bin/other\n" + b[2]
		}, after: "MAILTO=me\n0 * * * * /usr/bin/other\n"},
		{name: "a-crontab-of-only-ours-is-removed", crontab: func(h *moveHome) string { return schedCronBlock(h, "") }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron(row.crontab(h))
			res := schedUninstall(t, h)
			schedUninstalled(t, res)
			wantLines(t, "stdout", res.Stdout, schedCronRemoved)
			got, ok := stub.Crontab()
			if row.after == "" && ok || got != row.after {
				t.Errorf("crontab after = %q (present %v), want %q", got, ok, row.after)
			}
			if strings.Contains(got, schedMark) {
				t.Errorf("a tagged line is left:\n%s", got)
			}
		})
	}

	// A crontab holding only the schedule's marked comment (its jobs already
	// removed by hand): the comment goes, and the message does not count a job.
	t.Run("only-the-marked-comment-left", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("# " + schedMark + ": the find-missing and expire sweeps (install-agent-director skill, schedule/agent-director.cron)\n")
		res := schedUninstall(t, h)
		schedUninstalled(t, res)
		wantLines(t, "stdout", res.Stdout, "uninstall.sh: removed the schedule's marker comment from the crontab")
		if strings.Contains(res.Stdout, "schedule line(s)") {
			t.Errorf("stdout counts a job that was not there:\n%s", res.Stdout)
		}
		if got, ok := stub.Crontab(); ok {
			t.Errorf("crontab left = %q, want none", got)
		}
	})

	// A crontab it cannot read (not "no crontab for <user>") is reported, not
	// taken for an empty one, and nothing is written.
	t.Run("a-crontab-it-cannot-read-is-reported-and-the-exit-stays-0", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign + schedCronBlock(h, ""))
		h.Env["CRON_STUB_LIST_FAIL"] = "crontab: cannot open the spool"
		res := schedUninstall(t, h)
		schedUninstalled(t, res)
		wantLines(t, "stderr", res.Stderr,
			"uninstall.sh: could not read the crontab to remove the schedule (crontab -l failed, exit 1): crontab: cannot open the spool",
			"  If install.sh --schedule added lines tagged # "+schedMark+" to it, remove them by hand (crontab -e).")
		if strings.Contains(res.Stdout, "schedule line(s)") {
			t.Errorf("stdout claims lines were removed:\n%s", res.Stdout)
		}
		if stub.CronWrites() != 0 {
			t.Errorf("crontab was written after a failed read: calls %v", stub.CronCalls())
		}
		if got, _ := stub.Crontab(); got != foreign+schedCronBlock(h, "") {
			t.Errorf("crontab changed: %q", got)
		}
	})

	// Nothing of ours: nothing is said and the crontab is not written.
	t.Run("none-present-is-a-silent-no-op", func(t *testing.T) {
		t.Parallel()
		for _, crontab := range []string{"", foreign} {
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron(crontab)
			stub.Systemctl(0)
			res := schedUninstall(t, h)
			schedUninstalled(t, res)
			if strings.Contains(res.Stdout+res.Stderr, "schedule") || strings.Contains(res.Stdout+res.Stderr, "systemd") {
				t.Errorf("uninstall.sh says something about a schedule that was not there:\n%s\n%s", res.Stdout, res.Stderr)
			}
			for _, c := range stub.CronCalls() {
				if c != "-l" {
					t.Errorf("crontab got %q, want only -l", c)
				}
			}
			if got, _ := stub.Crontab(); got != crontab {
				t.Errorf("crontab = %q, want it unchanged (%q)", got, crontab)
			}
			// The unit directory is the manager's, which is asked once, so there is one
			// call even with no unit present; it disables and reloads nothing.
			if calls := stub.SystemctlCalls(); len(calls) != 1 || calls[0] != "--user show-environment" {
				t.Errorf("systemctl calls = %v, want only one show-environment", calls)
			}
		}
	})

	// busybox's `crontab -l` for a user with no crontab: "No such file or
	// directory" is "no crontab yet", not a failed read.
	t.Run("busybox-no-crontab-is-a-silent-no-op", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		h.Env["CRON_STUB_NOFILE_MSG"] = schedBusyboxNoCrontab
		res := schedUninstall(t, h)
		schedUninstalled(t, res)
		if strings.Contains(res.Stdout+res.Stderr, "crontab") || strings.Contains(res.Stdout+res.Stderr, "schedule") {
			t.Errorf("uninstall.sh says something about a crontab that was not there:\n%s\n%s", res.Stdout, res.Stderr)
		}
		for _, c := range stub.CronCalls() {
			if c != "-l" {
				t.Errorf("crontab got %q, want only -l", c)
			}
		}
	})

	t.Run("a-crontab-it-cannot-write-is-reported-and-the-exit-stays-0", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign + schedCronBlock(h, ""))
		h.Env["CRON_STUB_WRITE_FAIL"] = "crontab: permission denied"
		res := schedUninstall(t, h)
		schedUninstalled(t, res)
		wantLines(t, "stderr", res.Stderr,
			"uninstall.sh: could not remove the schedule from the crontab (exit 1): crontab: permission denied",
			"  Remove the 3 line(s) tagged # "+schedMark+" by hand (crontab -e).")
		if strings.Contains(res.Stdout, "removed 3 schedule line(s)") {
			t.Errorf("stdout claims the lines were removed:\n%s", res.Stdout)
		}
	})
}

// schedUnitDirCase is one rule for where the user manager reads units: the
// manager's environment (what its show-environment prints), the XDG_CONFIG_HOME
// of this shell, the directory that follows, and directories that must be left
// as they are.
type schedUnitDirCase struct {
	name  string
	shell string                     // this shell's XDG_CONFIG_HOME, under Root; "" unset
	mgr   func(h *moveHome) []string // the manager's environment
	dir   func(h *moveHome) string   // the unit directory
	other func(h *moveHome) []string // directories a run must not touch
}

// schedUnitDirCases are the cases of the rule: the manager's absolute
// XDG_CONFIG_HOME, else its absolute HOME, else this shell's.
var schedUnitDirCases = []schedUnitDirCase{
	{name: "manager-XDG_CONFIG_HOME-over-the-shells",
		shell: "shell-xdg",
		mgr:   func(h *moveHome) []string { return []string{"XDG_CONFIG_HOME=" + filepath.Join(h.Root, "mgr-xdg")} },
		dir:   func(h *moveHome) string { return filepath.Join(h.Root, "mgr-xdg", "systemd", "user") },
		other: func(h *moveHome) []string {
			return []string{schedUnitDir(h), filepath.Join(h.Root, "shell-xdg", "systemd", "user")}
		}},
	{name: "manager-HOME",
		mgr: func(h *moveHome) []string {
			return []string{"PATH=/usr/bin", "HOME=" + filepath.Join(h.Root, "mgr-home")}
		},
		dir:   func(h *moveHome) string { return filepath.Join(h.Root, "mgr-home", ".config", "systemd", "user") },
		other: func(h *moveHome) []string { return []string{schedUnitDir(h)} }},
	{name: "manager-XDG_CONFIG_HOME-over-its-HOME",
		mgr: func(h *moveHome) []string {
			return []string{"HOME=" + filepath.Join(h.Root, "mgr-home"), "XDG_CONFIG_HOME=" + filepath.Join(h.Root, "mgr-xdg")}
		},
		dir: func(h *moveHome) string { return filepath.Join(h.Root, "mgr-xdg", "systemd", "user") },
		other: func(h *moveHome) []string {
			return []string{schedUnitDir(h), filepath.Join(h.Root, "mgr-home", ".config", "systemd", "user")}
		}},
	{name: "the-shells-XDG_CONFIG_HOME-when-the-manager-says-neither",
		shell: "shell-xdg",
		mgr:   func(h *moveHome) []string { return []string{"PATH=/usr/bin"} },
		dir:   func(h *moveHome) string { return filepath.Join(h.Root, "shell-xdg", "systemd", "user") },
		other: func(h *moveHome) []string { return []string{schedUnitDir(h)} }},
}

func TestUninstallScheduleSystemd(t *testing.T) {
	rows := []struct {
		name   string
		showRC int
		out    string
		calls  []string // systemctl calls wanted, in order
	}{
		{name: "the-manager-answers", out: "uninstall.sh: disabled the agent-director systemd user timers and removed their units from {DIR}",
			calls: []string{"--user show-environment", "--user disable --now agent-director-find-missing.timer agent-director-expire.timer", "--user daemon-reload"}},
		{name: "the-manager-is-not-reachable", showRC: 1,
			out: "uninstall.sh: removed the agent-director systemd user units from {DIR} (the user manager was not reachable, so the timers were not disabled; they stop at its next start)"},
	}
	// Each row runs against the plain rule (the manager says nothing: this shell's
	// HOME) and against each way the manager's environment names the directory.
	cases := append([]schedUnitDirCase{{name: "the-shells-HOME",
		dir: schedUnitDir}}, schedUnitDirCases...)
	for _, row := range rows {
		for _, dc := range cases {
			if row.showRC != 0 && dc.name != "the-shells-HOME" {
				continue // an unreachable manager prints no environment
			}
			t.Run(row.name+"/"+dc.name, func(t *testing.T) {
				t.Parallel()
				h := newMoveHome(t)
				stub := schedStub{h}
				stub.Systemctl(row.showRC)
				if dc.mgr != nil {
					stub.ManagerEnv(dc.mgr(h)...)
				}
				if dc.shell != "" {
					h.Env["XDG_CONFIG_HOME"] = filepath.Join(h.Root, dc.shell)
				}
				dir := dc.dir(h)
				marked := "# " + schedMark + ": the expire sweep\n[Unit]\n"
				schedSeedUnitsIn(h, dir, marked)
				// A unit of the user's own under one of our names, and an unrelated one,
				// stay; the links to our timers go.
				own := filepath.Join(dir, "agent-director-expire.timer")
				h.Write(own, "[Timer]\nOnCalendar=hourly\n", 0o644)
				other := filepath.Join(dir, "other.service")
				h.Write(other, "[Unit]\n", 0o644)
				for _, tm := range []string{"agent-director-find-missing.timer", "agent-director-expire.timer"} {
					h.Link("../"+tm, filepath.Join(dir, "timers.target.wants", tm))
				}
				// Marked units in a directory the manager does not read are not ours to
				// touch from here: they are the rule's other answers.
				var decoys []string
				if dc.other != nil {
					for _, d := range dc.other(h) {
						schedSeedUnitsIn(h, d, marked)
						decoys = append(decoys, filepath.Join(d, "agent-director-find-missing.service"))
					}
				}
				res := schedUninstall(t, h)
				schedUninstalled(t, res)
				wantLines(t, "stdout", res.Stdout, strings.ReplaceAll(row.out, "{DIR}", dir))
				for _, f := range []string{"agent-director-find-missing.service", "agent-director-find-missing.timer", "agent-director-expire.service"} {
					if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
						t.Errorf("%s is still there", f)
					}
				}
				for _, kept := range append([]string{own, other}, decoys...) {
					if _, err := os.Stat(kept); err != nil {
						t.Errorf("%s was removed: %v", kept, err)
					}
				}
				for _, tm := range []string{"agent-director-find-missing.timer", "agent-director-expire.timer"} {
					if _, err := os.Lstat(filepath.Join(dir, "timers.target.wants", tm)); err == nil {
						t.Errorf("the wants link of %s is still there", tm)
					}
				}
				if row.calls != nil {
					wantLines(t, "systemctl calls", strings.Join(stub.SystemctlCalls(), "\n"), row.calls...)
				} else if got := stub.SystemctlCalls(); len(got) != 1 {
					t.Errorf("systemctl calls = %v, want only show-environment", got)
				}
			})
		}
	}
}

// TestUninstallFetchedSchedule: the copy of the schedule files a piped install
// kept goes, with its directory; a directory holding other files stays, with a note.
func TestUninstallFetchedSchedule(t *testing.T) {
	rows := []struct {
		name  string
		extra string // another file in the directory
		out   string
	}{
		{name: "only-the-five-files", out: "uninstall.sh: removed the fetched schedule files and {DIR}"},
		{name: "another-file-stays", extra: "mine.txt", out: "uninstall.sh: removed the fetched schedule files; left {DIR}, which holds other files"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			if err := copyDir(h.S.ScheduleSrc, schedFetchedDir(h)); err != nil {
				t.Fatal(err)
			}
			leftover := filepath.Join(h.New, ".schedule.AbC123")
			h.Write(filepath.Join(leftover, "agent-director.cron"), "# "+schedMark+"\n", 0o644)
			if row.extra != "" {
				h.Write(filepath.Join(schedFetchedDir(h), row.extra), "mine\n", 0o644)
			}
			res := schedUninstall(t, h)
			schedUninstalled(t, res)
			wantLines(t, "stdout", res.Stdout, strings.ReplaceAll(row.out, "{DIR}", schedFetchedDir(h)))
			for _, f := range schedFiles {
				if _, err := os.Stat(filepath.Join(schedFetchedDir(h), f)); err == nil {
					t.Errorf("%s is still there", f)
				}
			}
			if _, err := os.Stat(leftover); err == nil {
				t.Errorf("the fetch's temp directory %s is still there", leftover)
			}
			_, err := os.Stat(filepath.Join(schedFetchedDir(h), row.extra))
			if (row.extra != "") != (err == nil) {
				t.Errorf("the other file %q: stat error %v, want it kept exactly when there is one", row.extra, err)
			}
		})
	}
}

// TestUninstallUndoesTheInstall: what install.sh --schedule adds, uninstall.sh
// removes, by either mechanism and for a piped install, and the user's own
// crontab lines are as they were.
func TestUninstallUndoesTheInstall(t *testing.T) {
	foreign := "MAILTO=me\n# my own job\n0 * * * * /usr/bin/other\n"

	t.Run("cron-from-a-pipe", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign)
		stub.Curl()
		if res := schedRun(t, h, schedScript(t, h, false), true, "--schedule"); res.Code != 0 {
			t.Fatalf("install exit = %d\n%s", res.Code, res.Stderr)
		}
		if got, _ := stub.Crontab(); !strings.Contains(got, schedMark) {
			t.Fatalf("the install added no schedule:\n%s", got)
		}
		schedUninstalled(t, schedUninstall(t, h))
		if got, _ := stub.Crontab(); got != foreign {
			t.Errorf("crontab after uninstall = %q, want %q", got, foreign)
		}
		if _, err := os.Stat(schedFetchedDir(h)); err == nil {
			t.Errorf("%s is still there", schedFetchedDir(h))
		}
	})

	// What the install wrote in the manager's unit directory, the uninstall removes
	// from the same one, under each way that directory is named.
	for _, dc := range append([]schedUnitDirCase{{name: "the-shells-HOME", dir: schedUnitDir}}, schedUnitDirCases...) {
		t.Run("systemd/"+dc.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			schedStub{h}.Systemctl(0)
			if dc.mgr != nil {
				schedStub{h}.ManagerEnv(dc.mgr(h)...)
			}
			if dc.shell != "" {
				h.Env["XDG_CONFIG_HOME"] = filepath.Join(h.Root, dc.shell)
			}
			dir := dc.dir(h)
			if res := schedRun(t, h, schedScript(t, h, true), false, "--schedule"); res.Code != 0 {
				t.Fatalf("install exit = %d\n%s", res.Code, res.Stderr)
			}
			schedUnitsWrittenIn(t, dir)
			schedUninstalled(t, schedUninstall(t, h))
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				t.Errorf("%s is still in %s", e.Name(), dir)
			}
			if _, err := os.Lstat(filepath.Join(dir, "timers.target.wants", "agent-director-expire.timer")); err == nil {
				t.Errorf("a timer is still enabled")
			}
		})
	}
}
