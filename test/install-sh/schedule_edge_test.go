package installsh_test

// install.sh's schedule step, the cases that keep it honest on a host that is
// not the easy one (b.wur): no cron daemon, a HOME that is not the passwd
// entry's, values that need quoting, files that are not this version's, and a
// systemd install that fails half way. Helpers are schedule_test.go's.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	// schedNoDaemonStderr is the line a --schedule that put the cron lines in
	// with no cron daemon running prints on stderr.
	schedNoDaemonStderr = "  schedule: cron: find-missing every 2 minutes and expire daily at 03:17 added to this user's crontab, each line tagged # " + schedMark + ", but NO CRON DAEMON (a process named cron or crond) is running on this machine, so they do not run; start the cron service"
	// schedNoDaemonWhy is the start of the reason the warning gives for it.
	schedNoDaemonWhy = "no cron daemon (a process named cron or crond) is running on this machine, so the crontab lines just added do not run until one is started; systemd: "
	// schedNoDaemonCron is why cron was not used when it could not be (the
	// crontab works, nothing runs it).
	schedNoDaemonCron = "no cron daemon (a process named cron or crond) is running on this machine, so crontab lines would never run"
	schedControlWhy   = "a path to put in the schedule (the tmux directory, TMUX_TMPDIR, HOME or the binary) holds a control character"
)

// schedCronPrefix is the environment words before the command in the crontab
// lines of h's install, written as they are: PATH, then TMUX_TMPDIR when
// tmuxTmp is not "", then HOME when home is not "".
func schedCronPrefix(h *moveHome, tmuxTmp, home string) string {
	env := "PATH=" + h.S.Toolbox + ":/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if tmuxTmp != "" {
		env += " TMUX_TMPDIR=" + tmuxTmp
	}
	if home != "" {
		env += " HOME=" + home
	}
	return env
}

// schedUnitEnv is the Environment= lines of the units of h's install, with the
// values written as they are (tmpdir and home "" are left out).
func schedUnitEnv(h *moveHome, tmpdir, home string) string {
	env := `Environment="PATH=` + h.S.Toolbox + `:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"` + "\n"
	if tmpdir != "" {
		env += `Environment="TMUX_TMPDIR=` + tmpdir + `"` + "\n"
	}
	if home != "" {
		env += `Environment="HOME=` + home + `"` + "\n"
	}
	return env
}

var schedNoteLine = regexp.MustCompile(`(?m)^##`)

// schedUnitsWant fails the case unless the four unit files an install wrote are
// whole: no placeholder or note left, the marker, the sweeps' ExecStart after
// env, and the timers' schedules.
func schedUnitsWant(t *testing.T, h *moveHome, env string) {
	t.Helper()
	schedUnitsWantIn(t, h, schedUnitDir(h), env)
}

// schedUnitsWantIn is schedUnitsWant for the unit directory dir.
func schedUnitsWantIn(t *testing.T, h *moveHome, dir, env string) {
	t.Helper()
	units := schedUnitsWrittenIn(t, dir)
	canon := ownedCanonical(h)
	for f, body := range units {
		if strings.Contains(body, "@@") || schedNoteLine.MatchString(body) {
			t.Errorf("%s holds a placeholder or a note:\n%s", f, body)
		}
		if !strings.Contains(body, "# "+schedMark+":") {
			t.Errorf("%s lacks the marker:\n%s", f, body)
		}
	}
	for sweep, f := range map[string]string{"find-missing": "agent-director-find-missing.service", "expire": "agent-director-expire.service"} {
		if body := units[f]; !strings.Contains(body, "Type=oneshot\n"+env+"ExecStart="+canon+" "+sweep+"\n") {
			t.Errorf("%s wants Type=oneshot, then\n%sExecStart=%s %s; got:\n%s", f, env, canon, sweep, body)
		}
	}
	if body := units["agent-director-find-missing.timer"]; !strings.Contains(body, "OnCalendar=*:0/2\n") {
		t.Errorf("find-missing timer is not every 2 minutes:\n%s", body)
	}
	if body := units["agent-director-expire.timer"]; !strings.Contains(body, "OnCalendar=*-*-* 03:17:00\n") {
		t.Errorf("expire timer is not daily at 03:17:\n%s", body)
	}
}

// schedUnitFiles is the names of the files (not directories) in the user's
// unit directory, which is empty when it is not there.
func schedUnitFiles(h *moveHome) []string {
	entries, _ := os.ReadDir(schedUnitDir(h))
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// schedEdit rewrites the schedule file name beside script (a copy the case owns).
func schedEdit(t *testing.T, script, name string, edit func(string) string) {
	t.Helper()
	p := filepath.Join(filepath.Dir(script), "schedule", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(edit(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// schedRenderWant is a schedule file as it is installed: the notes (lines
// starting with two #) dropped, @@AGENT_DIRECTOR@@ and, in a cron file, @@ENV_PREFIX@@ or,
// in a unit, a line of just @@ENV_LINES@@ replaced.
func schedRenderWant(text, envOrPrefix, bin string, cron bool) string {
	out := ""
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "##"):
			continue
		case cron:
			l = strings.ReplaceAll(l, "@@ENV_PREFIX@@", envOrPrefix)
		case l == "@@ENV_LINES@@":
			out += envOrPrefix
			continue
		}
		out += strings.ReplaceAll(l, "@@AGENT_DIRECTOR@@", bin) + "\n"
	}
	return out
}

// TestScheduleNoDaemon: a crontab that `crontab -l` reads is no proof that cron
// runs. With no cron daemon, --schedule uses systemd; with no systemd either it
// still puts the lines in (they run once a daemon is started) but says loudly
// that they do not run now; and an install that finds only such lines warns.
func TestScheduleNoDaemon(t *testing.T) {
	foreign := "MAILTO=me\n0 * * * * /usr/bin/other\n"

	t.Run("systemd-takes-over-and-the-cron-lines-go", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign + schedCronBlock(h, ""))
		stub.Systemctl(0)
		h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, "  schedule: systemd: user timers agent-director-find-missing.timer (every 2 minutes) and agent-director-expire.timer (daily at 03:17) installed in "+schedUnitDir(h)+" and enabled.")
		schedUnitsWant(t, h, schedUnitEnv(h, "", ""))
		if got, _ := stub.Crontab(); got != foreign {
			t.Errorf("crontab = %q, want only the user's own lines (%q): the lines that never run were left in", got, foreign)
		}
	})

	t.Run("systemd-takes-over-and-leaves-an-unrelated-crontab-alone", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign)
		stub.Systemctl(0)
		h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		schedUnitsWant(t, h, schedUnitEnv(h, "", ""))
		if stub.CronWrites() != 0 {
			t.Errorf("a crontab with none of our lines was rewritten: %v", stub.CronCalls())
		}
	})

	whys := []struct {
		name    string
		sysctl  func(stub schedStub)
		systemd string
	}{
		{name: "no-systemctl", sysctl: func(schedStub) {}, systemd: "systemctl is not installed"},
		{name: "no-user-manager", sysctl: func(s schedStub) { s.Systemctl(1) },
			systemd: "the systemd user manager is not reachable (systemctl --user show-environment failed)"},
	}
	for _, row := range whys {
		t.Run("no-systemd-either-the-lines-go-in-and-the-warning-says-they-do-not-run/"+row.name, func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron(foreign)
			row.sysctl(stub)
			h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
			}
			if got, _ := stub.Crontab(); got != foreign+schedCronBlock(h, "") {
				t.Errorf("crontab =\n%s\nwant the user's lines, then ours", got)
			}
			wantLines(t, "stderr", res.Stderr, schedNoDaemonStderr, schedWarnHead,
				"  --schedule was asked for and could not schedule the sweeps: "+schedNoDaemonWhy+row.systemd)
			for _, bad := range []string{"This user's crontab holds this install's lines", "installed nothing"} {
				if strings.Contains(res.Stderr, bad) {
					t.Errorf("stderr holds %q, which is the other warning:\n%s", bad, res.Stderr)
				}
			}
			if strings.Contains(res.Stdout, "schedule: cron") || strings.Contains(res.Stdout, "found this install's") {
				t.Errorf("stdout says the schedule is in place:\n%s", res.Stdout)
			}
			if files := schedUnitFiles(h); len(files) != 0 {
				t.Errorf("units were left behind: %v", files)
			}
		})
	}

	// No daemon, the lines present, and no --schedule: the lines never fire, so
	// the install warns, says why, and does not call them found.
	t.Run("without---schedule-our-lines-and-no-daemon-warn", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(foreign + schedCronBlock(h, ""))
		stub.Systemctl(0)
		h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
		script := schedScript(t, h, true)
		res := schedRun(t, h, script, false)
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  This user's crontab holds this install's lines for both sweeps, but no cron daemon (a process named",
			"  cron or crond) is running on this machine, so they never run: start the cron service, or use systemd",
			"  user timers (the files below).",
			"  Schedule both now, as the agents' user (the user the agents run as, in their tmux environment). Run as")
		if strings.Contains(res.Stderr, "--schedule was asked for") {
			t.Errorf("a plain install says --schedule was asked for:\n%s", res.Stderr)
		}
		if strings.Contains(res.Stdout, "found this install's") {
			t.Errorf("stdout calls lines that never run found:\n%s", res.Stdout)
		}
		if stub.CronWrites() != 0 || slicesContainEnable(stub.SystemctlCalls()) {
			t.Errorf("a plain install changed the schedule: crontab %v, systemctl %v", stub.CronCalls(), stub.SystemctlCalls())
		}
	})

	// Units in place count with or without lines and a daemon.
	t.Run("the-four-units-count-even-with-no-daemon", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron(schedCronBlock(h, ""))
		stub.Systemctl(0)
		schedSeedUnits(h, "# "+schedMark+"\n")
		h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
		res := schedRun(t, h, schedScript(t, h, true), false)
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, schedFoundSystemd)
	})

	// Without the switch the daemon is looked for: with pgrep, a process named
	// cron or crond.
	pgrep := []struct {
		name   string
		script string
		found  bool
	}{
		{name: "pgrep-finds-crond", script: `[[ "$*" == "-x crond" ]] && exit 0; exit 1`, found: true},
		{name: "pgrep-finds-cron", script: `[[ "$*" == "-x cron" ]] && exit 0; exit 1`, found: true},
		{name: "pgrep-finds-neither", script: `exit 1`},
	}
	for _, row := range pgrep {
		t.Run("detected/"+row.name, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("the daemon is looked for with pgrep and /proc on Linux only")
			}
			t.Parallel()
			h := newMoveHome(t)
			schedStub{h}.Cron(schedCronBlock(h, ""))
			h.WriteTool("pgrep", row.script)
			h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = ""
			res := schedRun(t, h, schedScript(t, h, true), false)
			if res.Code != 0 {
				t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
			}
			if row.found {
				wantLines(t, "stdout", res.Stdout, schedFoundCron)
			}
			if got := strings.Contains(res.Stderr, schedWarnHead); got == row.found {
				t.Errorf("warning = %v with a daemon found = %v:\n%s", got, row.found, res.Stderr)
			}
		})
	}
}

// TestScheduleHome: cron and systemd give a job the home of the passwd entry,
// but the binary keeps its files in $HOME/.config/agent-director, so a $HOME that
// is not that entry's is carried into the lines and the units; one that is, is
// not, and the warning's lines carry what the install would write.
func TestScheduleHome(t *testing.T) {
	rows := []struct {
		name   string
		passwd func(h *moveHome) (string, bool) // INSTALL_SH_TEST_PASSWD_HOME; false: not set
		carry  bool
	}{
		{name: "another-passwd-home-is-carried", passwd: func(h *moveHome) (string, bool) { return "/home/someone-else", true }, carry: true},
		{name: "an-unknown-passwd-home-is-carried", passwd: func(h *moveHome) (string, bool) { return "", true }, carry: true},
		{name: "a-trailing-slash-is-the-same-home", passwd: func(h *moveHome) (string, bool) { return h.Home + "/", true }},
	}
	for _, row := range rows {
		home := func(h *moveHome) string {
			if row.carry {
				return h.Home
			}
			return ""
		}
		setup := func(h *moveHome) {
			if v, ok := row.passwd(h); ok {
				h.Env["INSTALL_SH_TEST_PASSWD_HOME"] = v
			}
		}

		t.Run(row.name+"/cron", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron("")
			setup(h)
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
			}
			want := schedCronBlockHome(h, "", home(h))
			if got, _ := stub.Crontab(); got != want {
				t.Errorf("crontab =\n%s\nwant\n%s", got, want)
			}
		})

		t.Run(row.name+"/systemd", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			schedStub{h}.Systemctl(0)
			h.Env["TMUX_TMPDIR"] = "/run/ad-tmux"
			setup(h)
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
			}
			schedUnitsWant(t, h, schedUnitEnv(h, "/run/ad-tmux", home(h)))
		})

		// The warning of a plain install prints the lines the install would add.
		t.Run(row.name+"/warning", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			schedStub{h}.Cron("")
			setup(h)
			res := schedRun(t, h, schedScript(t, h, true), false)
			if res.Code != 0 {
				t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
			}
			lines := schedCronLinesHome(h, "", home(h))
			wantLines(t, "stderr", res.Stderr, schedWarnHead,
				"  These crontab lines (crontab -e, as that user) run this install's binary:", "    "+lines[0], "    "+lines[1])
		})
	}
}

// TestScheduleEscaping: a value that is not a bare word is written so that cron
// and systemd read it back as it is: single-quoted in a crontab line with %
// written \%, double-quoted in a unit with \, " and % escaped and $ left.
func TestScheduleEscaping(t *testing.T) {
	rows := []struct {
		name string
		val  string // TMUX_TMPDIR
		cron string // as the crontab line holds it
		unit string // inside Environment="TMUX_TMPDIR=..."
	}{
		{name: "a-bare-word-stays-bare", val: "/run/ad-tmux_1.2:x,y=z+w@v", cron: "/run/ad-tmux_1.2:x,y=z+w@v", unit: "/run/ad-tmux_1.2:x,y=z+w@v"},
		{name: "a-space", val: "/run/a b", cron: `'/run/a b'`, unit: "/run/a b"},
		{name: "a-single-quote", val: "/run/it's", cron: `'/run/it'\''s'`, unit: "/run/it's"},
		{name: "a-percent", val: "/run/50%", cron: `'/run/50\%'`, unit: "/run/50%%"},
		{name: "a-double-quote-and-a-backslash", val: `/run/q"b\s`, cron: `'/run/q"b\s'`, unit: `/run/q\"b\\s`},
		{name: "a-dollar-sign", val: "/run/$HOME/`x`", cron: "'/run/$HOME/`x`'", unit: "/run/$HOME/`x`"},
		{name: "a-glob-and-a-semicolon", val: "/run/*;x", cron: `'/run/*;x'`, unit: "/run/*;x"},
	}
	for _, row := range rows {
		t.Run(row.name+"/cron", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron("")
			h.Env["TMUX_TMPDIR"] = row.val
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
				t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
			}
			if got, want := func() string { g, _ := stub.Crontab(); return g }(), schedCronBlock(h, row.cron); got != want {
				t.Errorf("crontab =\n%s\nwant\n%s", got, want)
			}
		})
		t.Run(row.name+"/systemd", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			schedStub{h}.Systemctl(0)
			h.Env["TMUX_TMPDIR"] = row.val
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
				t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
			}
			schedUnitsWant(t, h, schedUnitEnv(h, row.unit, ""))
		})
	}

	// The same value is in the lines a plain install's warning prints.
	t.Run("the-warning-prints-the-quoted-lines", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		schedStub{h}.Cron("")
		h.Env["TMUX_TMPDIR"] = "/run/it's 50%"
		res := schedRun(t, h, schedScript(t, h, true), false)
		lines := schedCronLines(h, `'/run/it'\''s 50\%'`)
		wantLines(t, "stderr", res.Stderr, schedWarnHead, "    "+lines[0], "    "+lines[1])
	})
}

// TestScheduleRefusals: a value the schedule files cannot carry, or a schedule
// file that is not this install.sh's, installs nothing and says why; the warning
// then says it could not build the lines, rather than printing some.
func TestScheduleRefusals(t *testing.T) {
	couldNot := func(h *moveHome, script, why string) string {
		return "  Could not build the crontab lines from the schedule files in " + filepath.Dir(script) + "/schedule/: " + why + "."
	}
	notBuilt := func(t *testing.T, res moveResult) {
		t.Helper()
		if strings.Contains(res.Stderr, "These crontab lines") {
			t.Errorf("the warning prints crontab lines it could not build:\n%s", res.Stderr)
		}
	}
	nothingInstalled := func(t *testing.T, h *moveHome, stub schedStub) {
		t.Helper()
		if text, ok := stub.Crontab(); ok || stub.CronWrites() != 0 {
			t.Errorf("a crontab was written: %q", text)
		}
		if files := schedUnitFiles(h); len(files) != 0 {
			t.Errorf("unit files were left: %v", files)
		}
	}

	t.Run("a-control-character-in-TMUX_TMPDIR", func(t *testing.T) {
		for _, sched := range []bool{true, false} {
			for _, mech := range []string{"cron", "systemd"} {
				t.Run(mech+"/schedule="+boolName(sched), func(t *testing.T) {
					t.Parallel()
					h := newMoveHome(t)
					stub := schedStub{h}
					if mech == "cron" {
						stub.Cron("")
					} else {
						stub.Systemctl(0)
					}
					h.Env["TMUX_TMPDIR"] = "/run/a\nb"
					script := schedScript(t, h, true)
					var args []string
					if sched {
						args = append(args, "--schedule")
					}
					res := schedRun(t, h, script, false, args...)
					if res.Code != 0 {
						t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
					}
					wantLines(t, "stderr", res.Stderr, schedWarnHead)
					if sched {
						wantLines(t, "stderr", res.Stderr, "  --schedule was asked for and installed nothing: "+schedControlWhy)
					}
					wantLines(t, "stderr", res.Stderr, couldNot(h, script, schedControlWhy))
					notBuilt(t, res)
					nothingInstalled(t, h, stub)
				})
			}
		}
	})

	// The binary lives in $HOME/.config/agent-director/bin: a HOME with a quote
	// or a backslash in it has no safe spelling in a unit's ExecStart or a cron line.
	for _, name := range []string{"it's", `a"b`, `a\b`} {
		t.Run("a-binary-path-with-"+map[string]string{"it's": "a-single-quote", `a"b`: "a-double-quote", `a\b`: "a-backslash"}[name], func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Cron("")
			stub.Systemctl(0)
			home := filepath.Join(h.Root, name)
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			h.Env["HOME"] = home
			canon := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")
			why := "the binary's path holds a quote or a backslash: " + canon
			script := schedScript(t, h, true)
			res := schedRun(t, h, script, false, "--schedule")
			if res.Code != 0 {
				t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
			}
			wantLines(t, "stderr", res.Stderr, schedWarnHead, "  --schedule was asked for and installed nothing: "+why, couldNot(h, script, why))
			notBuilt(t, res)
			if text, ok := stub.Crontab(); ok || stub.CronWrites() != 0 {
				t.Errorf("a crontab was written: %q", text)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "systemd")); err == nil {
				t.Errorf("a unit directory was made")
			}
		})
	}

	t.Run("an-unfilled-placeholder-in-the-cron-file", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("MAILTO=me\n")
		script := schedScript(t, h, true)
		bad := "1 1 * * * @@ENV_PREFIX@@ @@AGENT_DIRECTOR@@ @@NEW_THING@@ # " + schedMark
		schedEdit(t, script, "agent-director.cron", func(s string) string { return s + bad + "\n" })
		why := "agent-director.cron still holds an unfilled @@ placeholder once @@AGENT_DIRECTOR@@ and @@ENV_PREFIX@@ are filled, so it comes from another version than this install.sh: " + bad
		res := schedRun(t, h, script, false, "--schedule")
		if res.Code != 0 {
			t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  --schedule was asked for and installed nothing: cron: "+why+"; systemd: systemctl is not installed",
			couldNot(h, script, why))
		notBuilt(t, res)
		if got, _ := stub.Crontab(); got != "MAILTO=me\n" || stub.CronWrites() != 0 {
			t.Errorf("the crontab was changed: %q, calls %v", got, stub.CronCalls())
		}
	})

	// A note line may hold @@ (the shipped files' notes do, and install fine):
	// only an installed line counts. Here an installed line does.
	t.Run("an-unfilled-placeholder-in-a-unit", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Systemctl(0)
		script := schedScript(t, h, true)
		schedEdit(t, script, "agent-director-expire.service", func(s string) string {
			return strings.Replace(s, "Type=oneshot\n", "Type=oneshot\nUser=@@RUN_USER@@\n", 1)
		})
		why := "agent-director-expire.service still holds an unfilled @@ placeholder once @@AGENT_DIRECTOR@@ and a line of just @@ENV_LINES@@ are filled, so it comes from another version than this install.sh: User=@@RUN_USER@@"
		res := schedRun(t, h, script, false, "--schedule")
		if res.Code != 0 {
			t.Errorf("exit = %d, want 0\n%s", res.Code, res.Stderr)
		}
		// The cron file is whole, so the warning still prints its lines.
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  --schedule was asked for and installed nothing: cron: crontab is not installed; systemd: "+why,
			"  These crontab lines (crontab -e, as that user) run this install's binary:")
		if files := schedUnitFiles(h); len(files) != 0 {
			t.Errorf("a refused install left unit files: %v (units rendered before the refused one are removed too)", files)
		}
		nothingInstalled(t, h, stub)
	})
}

// boolName is true or false, for a subtest name.
func boolName(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestScheduleSystemdRollback: a systemctl step that fails puts back what was
// there before: a unit file the run replaced is restored, one it made is
// removed with its enable link; units an earlier run installed that this run did
// not touch stay.
func TestScheduleSystemdRollback(t *testing.T) {
	old := func(f string) string {
		return "# " + schedMark + ": " + f + " from an earlier install\n[Unit]\nDescription=old\n"
	}
	leftovers := func(t *testing.T, h *moveHome) {
		t.Helper()
		for _, n := range schedUnitFiles(h) {
			if strings.HasPrefix(n, ".") {
				t.Errorf("%s is left in %s", n, schedUnitDir(h))
			}
		}
	}
	reason := "systemctl --user could not enable the timers"

	for _, step := range []string{"daemon-reload", "enable", "restart"} {
		t.Run("a-failed-"+step+"-restores-all-four-earlier-units", func(t *testing.T) {
			t.Parallel()
			h := newMoveHome(t)
			stub := schedStub{h}
			stub.Systemctl(0)
			h.Env["SYSCTL_STUB_FAIL"] = step
			for _, f := range schedFiles[1:] {
				h.Write(filepath.Join(schedUnitDir(h), f), old(f), 0o644)
			}
			res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
			if res.Code != 0 {
				t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
			}
			for _, f := range schedFiles[1:] {
				b, err := os.ReadFile(filepath.Join(schedUnitDir(h), f))
				if err != nil || string(b) != old(f) {
					t.Errorf("%s = %q (%v), want the earlier install's file", f, b, err)
				}
			}
			leftovers(t, h)
			// All four are still there, so they are found; the install says it could not
			// re-point them, and the lines are not added (no cron here to hold them).
			wantLines(t, "stdout", res.Stdout, schedFoundSystemd)
			wantLines(t, "stderr", res.Stderr, "  schedule: --schedule could not re-point them at this install: cron: crontab is not installed; systemd: "+reason)
			if strings.Contains(res.Stderr, "WARNING") {
				t.Errorf("an install that found its units warns:\n%s", res.Stderr)
			}
		})
	}

	t.Run("a-failed-enable-with-some-earlier-units-restores-those-and-removes-the-rest", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		schedStub{h}.Systemctl(0)
		h.Env["SYSCTL_STUB_FAIL"] = "enable"
		kept := []string{"agent-director-find-missing.service", "agent-director-find-missing.timer"}
		for _, f := range kept {
			h.Write(filepath.Join(schedUnitDir(h), f), old(f), 0o644)
		}
		other := filepath.Join(schedUnitDir(h), "other.service")
		h.Write(other, "[Unit]\n", 0o644)
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		for _, f := range kept {
			if b, err := os.ReadFile(filepath.Join(schedUnitDir(h), f)); err != nil || string(b) != old(f) {
				t.Errorf("%s = %q (%v), want the earlier file", f, b, err)
			}
		}
		for _, f := range []string{"agent-director-expire.service", "agent-director-expire.timer"} {
			if _, err := os.Stat(filepath.Join(schedUnitDir(h), f)); err == nil {
				t.Errorf("%s, which this run made, was left", f)
			}
		}
		if _, err := os.Stat(other); err != nil {
			t.Errorf("an unrelated unit was removed: %v", err)
		}
		leftovers(t, h)
		wantLines(t, "stderr", res.Stderr, schedWarnHead,
			"  --schedule was asked for and installed nothing: cron: crontab is not installed; systemd: "+reason)
	})

	t.Run("a-failed-restart-with-no-earlier-units-leaves-nothing-and-no-link", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		schedStub{h}.Systemctl(0)
		h.Env["SYSCTL_STUB_FAIL"] = "restart"
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		if files := schedUnitFiles(h); len(files) != 0 {
			t.Errorf("unit files left: %v", files)
		}
		for _, tm := range []string{"agent-director-find-missing.timer", "agent-director-expire.timer"} {
			if _, err := os.Lstat(filepath.Join(schedUnitDir(h), "timers.target.wants", tm)); err == nil {
				t.Errorf("the enable link of %s was left", tm)
			}
		}
		wantLines(t, "stderr", res.Stderr, schedWarnHead, "  --schedule was asked for and installed nothing: cron: crontab is not installed; systemd: "+reason)
	})

	// With no cron daemon and units that a failed run put back, the cron lines are
	// not added: they and the units would both run.
	t.Run("restored-units-keep-the-cron-lines-out", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		stub.Systemctl(0)
		h.Env["SYSCTL_STUB_FAIL"] = "enable"
		h.Env["INSTALL_SH_TEST_CRON_RUNNING"] = "0"
		for _, f := range schedFiles[1:] {
			h.Write(filepath.Join(schedUnitDir(h), f), old(f), 0o644)
		}
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		if stub.CronWrites() != 0 {
			t.Errorf("cron lines were added next to the earlier units: %v", stub.CronCalls())
		}
		wantLines(t, "stdout", res.Stdout, schedFoundSystemd)
		wantLines(t, "stderr", res.Stderr, "  schedule: --schedule could not re-point them at this install: cron: "+schedNoDaemonCron+"; systemd: "+reason)
	})

	// A good run over units from an earlier install re-points them: the files are
	// the new ones, nothing of the backups is left.
	t.Run("an-upgrade-re-points-the-earlier-units", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Systemctl(0)
		for _, f := range schedFiles[1:] {
			h.Write(filepath.Join(schedUnitDir(h), f), old(f)+"ExecStart=/old/bin/agent-director sweep\n", 0o644)
		}
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		schedUnitsWant(t, h, schedUnitEnv(h, "", ""))
		leftovers(t, h)
		wantLines(t, "stdout", res.Stdout,
			"  schedule: systemd: user timers agent-director-find-missing.timer (every 2 minutes) and agent-director-expire.timer (daily at 03:17) installed in "+schedUnitDir(h)+" and enabled.")
		for _, f := range schedFiles[1:] {
			if b, _ := os.ReadFile(filepath.Join(schedUnitDir(h), f)); strings.Contains(string(b), "/old/bin") {
				t.Errorf("%s still names the old binary:\n%s", f, b)
			}
		}
	})

	// The found-but-could-not-re-point report is for any reason, not only the
	// enable: units in place and a --schedule that cannot use any mechanism.
	t.Run("found-units-and-a-schedule-that-cannot-be-redone", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		h.Env["XDG_CONFIG_HOME"] = filepath.Join(h.Root, "xdg")
		for _, f := range schedFiles[1:] {
			h.Write(filepath.Join(h.Root, "xdg", "systemd", "user", f), old(f), 0o644)
		}
		res := schedRun(t, h, schedScript(t, h, true), false, "--schedule")
		if res.Code != 0 || strings.Contains(res.Stderr, "WARNING") {
			t.Fatalf("exit = %d, stderr:\n%s", res.Code, res.Stderr)
		}
		wantLines(t, "stdout", res.Stdout, schedFoundSystemd)
		wantLines(t, "stderr", res.Stderr, "  schedule: --schedule could not re-point them at this install: cron: crontab is not installed; systemd: systemctl is not installed")
	})
}

// TestScheduleEditedFiles: the install writes the schedule files, filled in,
// and nothing else: an edit to a shipped file (a new schedule, a line added) is
// what the crontab and the units hold, and the unit directory holds the four
// files only.
func TestScheduleEditedFiles(t *testing.T) {
	t.Run("cron", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		stub := schedStub{h}
		stub.Cron("")
		script := schedScript(t, h, true)
		var edited string
		schedEdit(t, script, "agent-director.cron", func(s string) string {
			s = strings.Replace(s, "*/2 * * * *", "*/7 * * * *", 1)
			s += "5 4 * * 1 @@ENV_PREFIX@@ @@AGENT_DIRECTOR@@ find-missing --extra >/dev/null # " + schedMark + "\n"
			edited = s
			return s
		})
		res := schedRun(t, h, script, false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		want := schedRenderWant(edited, schedCronPrefix(h, "", ""), ownedCanonical(h), true)
		if !strings.Contains(want, "*/7 * * * *") || !strings.Contains(want, "--extra") {
			t.Fatalf("the test's own expectation lost the edit:\n%s", want)
		}
		if got, _ := stub.Crontab(); got != want {
			t.Errorf("crontab =\n%s\nwant the edited file, filled in:\n%s", got, want)
		}
	})

	t.Run("units", func(t *testing.T) {
		t.Parallel()
		h := newMoveHome(t)
		schedStub{h}.Systemctl(0)
		script := schedScript(t, h, true)
		edited := map[string]string{}
		for f, edit := range map[string]func(string) string{
			"agent-director-find-missing.timer":   func(s string) string { return strings.Replace(s, "OnCalendar=*:0/2", "OnCalendar=*:0/9", 1) },
			"agent-director-expire.service":       func(s string) string { return strings.Replace(s, "ExecStart=", "Nice=7\nExecStart=", 1) },
			"agent-director-find-missing.service": func(s string) string { return s },
			"agent-director-expire.timer":         func(s string) string { return s },
		} {
			schedEdit(t, script, f, func(s string) string { s = edit(s); edited[f] = s; return s })
		}
		res := schedRun(t, h, script, false, "--schedule")
		if res.Code != 0 {
			t.Fatalf("exit = %d\n%s", res.Code, res.Stderr)
		}
		env := schedUnitEnv(h, "", "")
		for f, text := range edited {
			want := schedRenderWant(text, env, ownedCanonical(h), false)
			if got, _ := os.ReadFile(filepath.Join(schedUnitDir(h), f)); string(got) != want {
				t.Errorf("%s =\n%s\nwant the edited file, filled in:\n%s", f, got, want)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(schedUnitDir(h), "agent-director-find-missing.timer")); !strings.Contains(string(b), "OnCalendar=*:0/9\n") {
			t.Errorf("the edit to the timer is not in the unit:\n%s", b)
		}
		if got, want := strings.Join(schedUnitFiles(h), " "), strings.Join(schedFiles[1:], " "); !sameWords(got, want) {
			t.Errorf("unit directory holds %q, want exactly %q", got, want)
		}
	})
}

// sameWords reports whether a and b hold the same space-separated words, in any order.
func sameWords(a, b string) bool {
	count := map[string]int{}
	for _, w := range strings.Fields(a) {
		count[w]++
	}
	for _, w := range strings.Fields(b) {
		count[w]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}
