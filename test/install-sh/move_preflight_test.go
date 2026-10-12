package installsh_test

// What install.sh does before it moves ~/.agent-director to
// ~/.config/agent-director (SR-11.7): the layouts it refuses (ErrMoveConflict,
// ErrAgentsLive, both exit 5, and an earlier release's pair, exit 3) leaving
// the whole home tree as it was, and the layouts it accepts.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// moveRefusal is one row of a refusal table: a layout, what holds the install
// at the moment it looks, and what it must say. The home tree before the run
// and after it (every holder released) must be the same by SR-14.3's rule.
type moveRefusal struct {
	name string
	// setup builds the layout, before the first snapshot; it may start processes.
	setup func(t *testing.T, h *moveHome)
	// hold runs after the snapshot and before install.sh: a lock or a mode
	// that is not part of the layout. Release undoes it.
	hold func(t *testing.T, h *moveHome)
	pair func(s *moveShared) movePair // nil: the pair built from this tree
	env  map[string]string
	// ignore names paths the run legitimately changes, relative to HOME.
	ignore []string
	want   moveWant
}

// runMoveRefusals runs each row: its layout, install.sh, and the checks. A
// want string may name {HOME}, {main} and {admin} (the pair's paths), and what
// setup put in h.Vars.
func runMoveRefusals(t *testing.T, rows []moveRefusal) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			if row.pair != nil {
				h.Pair = row.pair(h.S)
			}
			h.Vars["main"], h.Vars["admin"] = h.Pair.Main, h.Pair.Admin
			for k, v := range row.env {
				h.Env[k] = v
			}
			row.setup(t, h)
			before := h.Snapshot(row.ignore...)
			if row.hold != nil {
				row.hold(t, h)
			}
			res := h.Install()
			h.Release()
			if d := moveDiff(before, h.Snapshot(row.ignore...)); d != "" {
				t.Errorf("install.sh changed the home tree:\n%s", d)
			}
			row.want.Check(t, h, res)
		})
	}
}

// moveConfig is an old install's config.toml with [store] db_path (when not
// empty) and busy_timeout_ms set.
func moveConfig(dbPath string, busyMs int) string {
	s := "# agent-director config\n[defaults]\ninject_help_hook = true\n\n[store]\n"
	if dbPath != "" {
		s += fmt.Sprintf("db_path = %q\n", dbPath)
	}
	return s + fmt.Sprintf("busy_timeout_ms = %d\n", busyMs)
}

// moveSetAside moves what is at Home/from to Home/to (the directories made).
func moveSetAside(t *testing.T, h *moveHome, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(h.Home, to)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(h.Home, from), filepath.Join(h.Home, to)); err != nil {
		t.Fatal(err)
	}
}

// moveSetTime sets the modification time of path (a missing path fails the case).
func moveSetTime(t *testing.T, path, value string) {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

// moveOldWith builds the old install with opts, then runs more on it.
func moveOldWith(more func(t *testing.T, h *moveHome), opts ...moveOldOpt) func(t *testing.T, h *moveHome) {
	return func(t *testing.T, h *moveHome) {
		h.BuildOld(opts...)
		if more != nil {
			more(t, h)
		}
	}
}

func TestMoveConflictRefusals(t *testing.T) {
	conflict := func(stderr ...string) moveWant {
		return moveWant{Code: 5, Err: "ErrMoveConflict", Stderr: append([]string{"Nothing was installed or changed."}, stderr...)}
	}
	elsewhere := func(h *moveHome, name string) string { return filepath.Join(h.Home, "elsewhere", name) }
	// bothPresent builds an old install with known store times and a new directory beside it.
	bothPresent := func(newDir func(t *testing.T, h *moveHome)) func(t *testing.T, h *moveHome) {
		return moveOldWith(func(t *testing.T, h *moveHome) {
			moveSetTime(t, filepath.Join(h.Old, "state.db"), "2024-03-05 06:07:08")
			moveSetTime(t, filepath.Join(h.Old, "state.db-wal"), "2024-03-06 11:12:13")
			if err := os.MkdirAll(h.New, 0o755); err != nil {
				t.Fatal(err)
			}
			newDir(t, h)
		})
	}
	both := "both {HOME}/.agent-director and {HOME}/.config/agent-director exist; refusing to install."
	rows := []moveRefusal{
		{
			name:  "new-dir-is-a-file-fresh",
			setup: func(t *testing.T, h *moveHome) { h.Write(h.New, "not a directory\n", 0o644) },
			want:  conflict("{HOME}/.config/agent-director is not a directory; refusing to install.", ".aside-"),
		},
		{
			name:  "new-dir-is-a-file-with-old-install",
			setup: moveOldWith(func(t *testing.T, h *moveHome) { h.Write(h.New, "not a directory\n", 0o644) }),
			want:  conflict("{HOME}/.config/agent-director is not a directory; refusing to install.", ".aside-"),
		},
		{
			name:  "new-dir-is-a-dangling-link-fresh",
			setup: func(t *testing.T, h *moveHome) { h.Link(filepath.Join(h.Home, "gone"), h.New) },
			want:  conflict("{HOME}/.config/agent-director is a dangling link; refusing to install.", "which does not exist", "rm -- "),
		},
		{
			name:  "new-dir-is-a-dangling-link-with-old-install",
			setup: moveOldWith(func(t *testing.T, h *moveHome) { h.Link(filepath.Join(h.Home, "gone"), h.New) }),
			want:  conflict("{HOME}/.config/agent-director is a dangling link; refusing to install.", "which does not exist"),
		},
		{
			name:  "old-dir-is-a-dangling-link-that-is-not-the-compat-link",
			setup: func(t *testing.T, h *moveHome) { h.Link(filepath.Join(h.Home, "gone"), h.Old) },
			want:  conflict("{HOME}/.agent-director is a dangling link; refusing to install.", "is not the compatibility link to {HOME}/.config/agent-director"),
		},
		{
			name:  "old-dir-is-a-relative-dangling-link-to-another-place",
			setup: func(t *testing.T, h *moveHome) { h.Link(".config/agent-director-other", h.Old) },
			want:  conflict("{HOME}/.agent-director is a dangling link; refusing to install."),
		},
		{
			name: "both-present-new-holds-only-templates",
			setup: bothPresent(func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.New, "templates", "made.toml"), "name = \"made\"\n", 0o644)
			}),
			env: map[string]string{"TZ": "UTC"},
			want: moveWant{Code: 5, Err: "ErrMoveConflict", Stderr: []string{both,
				"old: {HOME}/.agent-director (store last changed: 2024-03-06 11:12:13)",
				"new: {HOME}/.config/agent-director (store last changed: no store)",
				"A new directory holding only what make-template created",
				"Nothing was installed or changed."},
				StderrRE: []string{`(?m)^    ?mv -- {HOME}/\.config/agent-director {HOME}/\.config/agent-director\.aside-\d{8}-\d{6}$`,
					`(?m)^    ?mv -- {HOME}/\.agent-director {HOME}/\.agent-director\.aside-\d{8}-\d{6}$`}},
		},
		{
			name: "both-present-new-holds-a-store",
			setup: bothPresent(func(t *testing.T, h *moveHome) {
				h.WriteStore(filepath.Join(h.New, "state.db"))
			}),
			// The snapshot's read-only read leaves an empty -wal, which counts for
			// the store's last change, so the times are set after it.
			hold: func(t *testing.T, h *moveHome) {
				for _, f := range []string{"state.db", "state.db-wal"} {
					if _, err := os.Lstat(filepath.Join(h.New, f)); err == nil {
						moveSetTime(t, filepath.Join(h.New, f), "2023-12-31 23:59:58")
					}
				}
			},
			env: map[string]string{"TZ": "UTC"},
			want: conflict(both,
				"old: {HOME}/.agent-director (store last changed: 2024-03-06 11:12:13)",
				"new: {HOME}/.config/agent-director (store last changed: 2023-12-31 23:59:58)"),
		},
		{
			name: "old-dir-is-a-link-to-a-directory",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveSetAside(t, h, ".agent-director", "elsewhere/ad")
				h.Link(elsewhere(h, "ad"), h.Old)
			}),
			want: conflict("{HOME}/.agent-director is a link; refusing to install.", "cp -R -- {HOME}/elsewhere/ad {HOME}/.agent-director.copy"),
		},
		{
			name:  "old-dir-is-a-file",
			setup: func(t *testing.T, h *moveHome) { h.Write(h.Old, "not a directory\n", 0o644) },
			want:  conflict("{HOME}/.agent-director is not a directory; refusing to install.", ".aside-"),
		},
		{
			name: "linked-config-toml",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveSetAside(t, h, ".agent-director/config.toml", "elsewhere/config.toml")
				h.Link(elsewhere(h, "config.toml"), filepath.Join(h.Old, "config.toml"))
			}),
			want: conflict("{HOME}/.agent-director/config.toml is a link; refusing to install.", "cp -- {HOME}/elsewhere/config.toml "),
		},
		{
			name: "linked-templates-dir",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveSetAside(t, h, ".agent-director/templates", "elsewhere/templates")
				h.Link(elsewhere(h, "templates"), filepath.Join(h.Old, "templates"))
			}),
			want: conflict("{HOME}/.agent-director/templates is a link; refusing to install.", "cp -R -- {HOME}/elsewhere/templates "),
		},
		{
			name: "linked-template",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveSetAside(t, h, ".agent-director/templates/worker.toml", "elsewhere/worker.toml")
				h.Link(elsewhere(h, "worker.toml"), filepath.Join(h.Old, "templates", "worker.toml"))
			}),
			want: conflict("template {HOME}/.agent-director/templates/worker.toml is a link; refusing to install."),
		},
		{
			name: "dangling-template-link",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				h.Link(filepath.Join(h.Home, "gone.toml"), filepath.Join(h.Old, "templates", "dangling.toml"))
			}),
			want: conflict("template {HOME}/.agent-director/templates/dangling.toml is a link; refusing to install.", "It is a dangling link", "rm -- "),
		},
		{
			name: "store-inside-the-old-dir-is-a-link-to-a-place-inside-it",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				h.Link(filepath.Join(h.Old, "real", "store.db"), filepath.Join(h.Old, "state.db"))
			}, withMoveStore(func(h *moveHome) string { return filepath.Join(h.Old, "real", "store.db") })),
			want: conflict("the store {HOME}/.agent-director/state.db is a link; refusing to install.", "SQLite's backup"),
		},
		{
			// SR-11.7 judges "inside" by the path before links resolve, so this is
			// refused too; install.sh classes it "warn" and moves it.
			name: "store-inside-the-old-dir-is-a-link-to-a-place-outside-it",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				h.Link(filepath.Join(h.Home, "data", "state.db"), filepath.Join(h.Old, "state.db"))
			}, withMoveStore(func(h *moveHome) string { return filepath.Join(h.Home, "data", "state.db") })),
			want: conflict("the store {HOME}/.agent-director/state.db is a link; refusing to install.", "SQLite's backup"),
		},
		{
			name: "store-reached-only-through-an-outside-link",
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				h.Link(filepath.Join(h.Old, "state.db"), filepath.Join(h.Home, "data", "state.db"))
			}, withMoveConfig(func(h *moveHome) string { return moveConfig(filepath.Join(h.Home, "data", "state.db"), 5000) })),
			want: conflict("the store {HOME}/data/state.db is a link; refusing to install."),
		},
		{
			name: "store-key-in-a-missing-directory",
			setup: moveOldWith(nil, withMoveNoStore(),
				withMoveConfig(func(h *moveHome) string { return moveConfig(filepath.Join(h.Home, "nowhere", "state.db"), 5000) })),
			want: conflict("cannot place [store] db_path {HOME}/nowhere/state.db against the old directory; refusing to install.", "mkdir -p -- {HOME}/nowhere"),
		},
	}
	runMoveRefusals(t, rows)
}

// moveStubProcs writes stand-ins for ps and lsof, whose output is ps and lsof
// (each a script body; "exit 1" for a command that fails).
func moveStubProcs(h *moveHome, ps, lsof string) {
	h.WriteTool("ps", ps)
	h.WriteTool("lsof", lsof)
}

func TestMoveAgentsLiveRefusals(t *testing.T) {
	live := func(stderr ...string) moveWant {
		return moveWant{Code: 5, Err: "ErrAgentsLive", Stderr: append([]string{
			"install.sh: AD's old directory is in use; refusing to upgrade while an agent or agent-director process runs.",
			"old binary: {HOME}/.agent-director/bin/agent-director",
			"Nothing was installed or changed."}, stderr...)}
	}
	oldBin := func(h *moveHome) string { return filepath.Join(h.Old, "bin", "agent-director") }
	// serving starts the stand-in at the path start returns, and records its pid.
	serving := func(start func(t *testing.T, h *moveHome) string) func(t *testing.T, h *moveHome) {
		return func(t *testing.T, h *moveHome) {
			h.BuildOld()
			h.Vars["pid"] = fmt.Sprint(h.StartServe(start(t, h)).Pid)
		}
	}
	running := "Processes running the old binary (pid, executable):"
	macos := map[string]string{"INSTALL_SH_TEST_PROC_CHECK": "macos"}
	psOut := func(pid int, exe string) string {
		return fmt.Sprintf("printf '%%s\\n' '  1 /sbin/init' '  %d %s'", pid, exe)
	}
	noCheck := "The process check did not run (this host has no /proc and no usable ps or lsof)."

	var rows []moveRefusal
	for _, state := range []string{"pending", "waiting", "working", "check_permission", "ask_user"} {
		rows = append(rows, moveRefusal{
			name:  "row-" + state,
			setup: moveOldWith(nil, withMoveLive(state)),
			want:  live("Rows in a live state (state, instance id):", "    "+state+" live-"+state, "Stop whatever starts agents"),
		})
	}
	rows = append(rows,
		moveRefusal{
			name:  "rows-in-every-live-state-are-all-named",
			setup: moveOldWith(nil, withMoveLive("pending", "waiting", "working", "check_permission", "ask_user")),
			want: live("    ask_user live-ask_user", "    check_permission live-check_permission", "    pending live-pending",
				"    waiting live-waiting", "    working live-working"),
		},
		moveRefusal{
			name: "second-check-live-row-appears-after-pre-flight",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				h.Env["MOVE_TEST_FLAG"] = filepath.Join(h.Root, "inserted")
				h.WrapTool("mktemp", `if [[ "${!#}" == *agent-director.stage.* && ! -e "$MOVE_TEST_FLAG" ]]; then
    : > "$MOVE_TEST_FLAG"
    `+moveQuote(h.S.SQLite)+` -batch -init /dev/null `+moveQuote(filepath.Join(h.Old, "state.db"))+` `+moveQuote(moveLiveRow("working"))+`
fi`)
			},
			// The insert is the test's: it changes the store (and its close
			// checkpoints the -wal away); nothing else may change.
			ignore: []string{".agent-director/state.db", ".agent-director/state.db-wal"},
			want: moveWant{Code: 5, Err: "ErrAgentsLive", Stdout: []string{"install.sh: pre-flight OK"},
				Stderr: []string{"    working live-working", "Nothing was installed or changed."}},
		},
		moveRefusal{
			name:  "process-started-by-its-path",
			setup: serving(func(t *testing.T, h *moveHome) string { return oldBin(h) }),
			want:  live(running, "    {pid} {HOME}/.agent-director/bin/agent-director"),
		},
		moveRefusal{
			name: "process-started-through-a-link",
			setup: serving(func(t *testing.T, h *moveHome) string {
				link := filepath.Join(h.Home, ".local", "bin", "agent-director")
				h.Link(oldBin(h), link)
				return link
			}),
			want: live(running, "    {pid} {HOME}/.agent-director/bin/agent-director"),
		},
		moveRefusal{
			name: "old-binary-is-a-link-to-a-running-file-elsewhere",
			setup: serving(func(t *testing.T, h *moveHome) string {
				other := filepath.Join(h.Home, "elsewhere", "agent-director")
				h.Copy(h.S.OldBinary, other, 0o755)
				if err := os.Remove(oldBin(h)); err != nil {
					t.Fatal(err)
				}
				h.Link(other, oldBin(h))
				return other
			}),
			want: live(running, "    {pid} {HOME}/elsewhere/agent-director"),
		},
		moveRefusal{
			name: "process-replaced-since-it-started",
			setup: func(t *testing.T, h *moveHome) {
				serving(func(t *testing.T, h *moveHome) string { return oldBin(h) })(t, h)
				// The way an upgrade replaces a running binary: a new file renamed over it.
				h.Copy(h.S.OldBinary, oldBin(h)+".new", 0o755)
				if err := os.Rename(oldBin(h)+".new", oldBin(h)); err != nil {
					t.Fatal(err)
				}
			},
			want: live(running, "    {pid} {HOME}/.agent-director/bin/agent-director"),
		},
		moveRefusal{
			name: "macos-ps-lists-the-old-binary",
			env:  macos,
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				moveStubProcs(h, psOut(4242, oldBin(h)), "exit 1")
			},
			want: live(running, "    4242 {HOME}/.agent-director/bin/agent-director"),
		},
		moveRefusal{
			name: "macos-ps-lists-a-link-into-the-old-bin",
			env:  macos,
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				link := filepath.Join(h.Home, ".local", "bin", "agent-director")
				h.Link(oldBin(h), link)
				moveStubProcs(h, psOut(4343, link), "exit 1")
			},
			want: live(running, "    4343 {HOME}/.local/bin/agent-director"),
		},
		moveRefusal{
			name: "macos-ps-fails-and-lsof-lists-the-old-binary",
			env:  macos,
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				moveStubProcs(h, "exit 1", "printf '%s\\n' p1 n/sbin/init p5151 n"+oldBin(h))
			},
			want: live(running, "    5151 {HOME}/.agent-director/bin/agent-director"),
		},
		moveRefusal{
			name: "macos-no-process-check-runs-with-a-live-row",
			env:  macos,
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld(withMoveLive("pending"))
				moveStubProcs(h, "exit 1", "exit 1")
			},
			want: live("    pending live-pending", noCheck),
		},
		moveRefusal{
			name:  "no-process-check-with-a-live-row",
			env:   map[string]string{"INSTALL_SH_TEST_PROC_CHECK": "none"},
			setup: moveOldWith(nil, withMoveLive("waiting")),
			want:  live("    waiting live-waiting", noCheck),
		},
		moveRefusal{
			name: "rows-cannot-be-read-store-busy-past-the-timeout",
			setup: moveOldWith(nil, withMoveRollbackStore(),
				withMoveConfig(func(*moveHome) string { return moveConfig("", 200) })),
			hold: func(t *testing.T, h *moveHome) { h.HoldSQLite(filepath.Join(h.Old, "state.db"), "BEGIN EXCLUSIVE;") },
			want: live("The live rows could not be read (the store was busy past its busy timeout of 200 ms, or is unreadable)",
				"A re-run reads them again.", "database is locked"),
		},
		moveRefusal{
			name: "rows-cannot-be-read-store-is-not-a-database",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld(withMoveNoStore())
				h.Write(filepath.Join(h.Old, "state.db"), "this is not a database, but it is more than a header's worth of text\n", 0o600)
			},
			want: live("The live rows could not be read", "A re-run reads them again.", "file is not a database"),
		},
		moveRefusal{
			name:  "rows-cannot-be-read-store-is-unreadable",
			setup: moveOldWith(nil),
			hold: func(t *testing.T, h *moveHome) {
				if os.Geteuid() == 0 {
					t.Skip("root reads a mode 000 file")
				}
				db := filepath.Join(h.Old, "state.db")
				fi, err := os.Stat(db)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(db, 0); err != nil {
					t.Fatal(err)
				}
				h.OnRelease(func() { _ = os.Chmod(db, fi.Mode().Perm()) })
			},
			want: live("The live rows could not be read", "A re-run reads them again.", "unable to open database file"),
		},
	)
	runMoveRefusals(t, rows)
}

// TestMovePreflightPasses covers the layouts the live check lets through: an
// earlier release's pair then ends the install at exit 3, after "pre-flight
// OK", with nothing moved or changed.
func TestMovePreflightPasses(t *testing.T) {
	earlier := func(s *moveShared) movePair { return s.NoConfigDir }
	// passes: pre-flight ends OK and no move starts; noted says whether stdout
	// carries the note that no process check could run.
	passes := func(noted bool) moveWant {
		const note = "process check: did not run (this host has no /proc and no usable ps or lsof), so no agent-director process was looked for"
		w := moveWant{Code: 3, Stdout: []string{"install.sh: pre-flight OK"}, NoStdout: []string{"move    :"}}
		if noted {
			w.Stdout = append(w.Stdout, note)
		} else {
			w.NoStdout = append(w.NoStdout, "process check: did not run")
		}
		return w
	}
	macos := map[string]string{"INSTALL_SH_TEST_PROC_CHECK": "macos"}
	rows := []moveRefusal{
		{
			name:  "linux-process-check-runs-and-finds-nothing",
			pair:  earlier,
			setup: moveOldWith(nil),
			want:  passes(false),
		},
		{
			name: "a-stand-in-running-from-another-path-is-not-the-old-binary",
			pair: earlier,
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				other := filepath.Join(h.Home, "elsewhere", "agent-director")
				h.Copy(h.S.OldBinary, other, 0o755)
				h.StartServe(other)
			}),
			want: passes(false),
		},
		{
			name: "macos-ps-lists-other-processes-only",
			pair: earlier,
			env:  macos,
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveStubProcs(h, "printf '%s\\n' '  1 /sbin/init' '  77 "+filepath.Join(h.Home, "elsewhere", "agent-director")+"'", "exit 1")
			}),
			want: passes(false),
		},
		{
			name: "macos-no-process-check-runs",
			pair: earlier,
			env:  macos,
			setup: moveOldWith(func(t *testing.T, h *moveHome) {
				moveStubProcs(h, "exit 1", "exit 1")
			}),
			want: passes(true),
		},
		{
			name:  "no-process-check-by-switch",
			pair:  earlier,
			env:   map[string]string{"INSTALL_SH_TEST_PROC_CHECK": "none"},
			setup: moveOldWith(nil),
			want:  passes(true),
		},
		{
			name: "a-stand-in-running-on-a-fresh-install-is-not-looked-for",
			pair: earlier,
			setup: func(t *testing.T, h *moveHome) {
				// A fresh install: no old directory, so no move is due and no check runs.
				other := filepath.Join(h.Home, "elsewhere", "agent-director")
				h.Copy(h.S.OldBinary, other, 0o755)
				h.StartServe(other)
			},
			want: passes(false),
		},
	}
	runMoveRefusals(t, rows)
}

func TestMoveEarlierReleaseRefused(t *testing.T) {
	pairs := func(main, admin func(s *moveShared) string) func(s *moveShared) movePair {
		return func(s *moveShared) movePair { return movePair{main(s), admin(s)} }
	}
	cur := func(s *moveShared) string { return s.Current.Main }
	curAdmin := func(s *moveShared) string { return s.Current.Admin }
	none := func(s *moveShared) string { return s.NoConfigDir.Main }
	other := func(s *moveShared) string { return s.OtherDir.Main }
	earlier := func(main, admin string) moveWant {
		return moveWant{Code: 3, Stdout: []string{"install.sh: pre-flight OK"}, NoStdout: []string{"move    :"},
			Stderr: []string{
				"install.sh: a binary to install is from an earlier release (its version output lacks config_dir ~/.config/agent-director); refusing to install.",
				"  agent-director      : {main} (config_dir: " + main + ")",
				"  agent-director-admin: {admin} (config_dir: " + admin + ")",
				"rerun with --from-release (omit --binary and --admin-binary)",
			}}
	}
	old := moveOldWith(nil)
	rows := []moveRefusal{
		{name: "neither-binary-has-config-dir", pair: pairs(none, none), setup: old, want: earlier("<none>", "<none>")},
		{name: "both-name-another-dir", pair: pairs(other, other), setup: old, want: earlier("~/.agent-director", "~/.agent-director")},
		{name: "only-the-main-binary-lacks-config-dir", pair: pairs(none, curAdmin), setup: old, want: earlier("<none>", "~/.config/agent-director")},
		{name: "only-the-admin-binary-names-another-dir", pair: pairs(cur, other), setup: old, want: earlier("~/.config/agent-director", "~/.agent-director")},
		{name: "fresh-install", pair: pairs(none, none), setup: func(*testing.T, *moveHome) {}, want: earlier("<none>", "<none>")},
	}
	runMoveRefusals(t, rows)
}

// moveListJSON runs the installed agent-director's list and returns its JSON.
func moveListJSON(t *testing.T, h *moveHome, bin string) {
	t.Helper()
	res := h.Run(bin, "list")
	if res.Code != 0 || !json.Valid([]byte(res.Stdout)) {
		t.Errorf("%s list: exit %d, stdout %q, stderr %q", bin, res.Code, res.Stdout, res.Stderr)
	}
}

func TestMoveAcceptedLayouts(t *testing.T) {
	installed := func(t *testing.T, h *moveHome, args ...string) moveResult {
		t.Helper()
		res := h.Install(args...)
		if res.Code != 0 {
			t.Fatalf("install.sh exit %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		return res
	}
	linkTarget := func(t *testing.T, link string) string {
		t.Helper()
		got, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("%s is not a link: %v", link, err)
		}
		return got
	}
	noKept := func(t *testing.T, h *moveHome) {
		t.Helper()
		if kept, _ := filepath.Glob(filepath.Join(h.Home, ".agent-director.pre-move-*")); len(kept) > 0 {
			t.Errorf("a move ran: %v", kept)
		}
	}
	rows := []struct {
		name string
		run  func(t *testing.T, h *moveHome)
	}{
		{"linked-new-dir-on-a-fresh-install", func(t *testing.T, h *moveHome) {
			target := filepath.Join(h.Home, "data", "ad-config")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			h.Link(target, h.New)
			installed(t, h)
			if got := linkTarget(t, h.New); got != target {
				t.Errorf("new dir link = %q, want %q", got, target)
			}
			moveListJSON(t, h, filepath.Join(target, "bin", "agent-director"))
		}},
		{"linked-new-dir-on-a-re-install-after-the-move", func(t *testing.T, h *moveHome) {
			h.BuildOld(withMoveNoStore())
			installed(t, h)
			kept, _ := filepath.Glob(filepath.Join(h.Home, ".agent-director.pre-move-*"))
			if len(kept) != 1 {
				t.Fatalf("kept directories after the move = %v, want one", kept)
			}
			// The new directory now lives elsewhere and is reached through a link.
			target := filepath.Join(h.Home, "data", "ad-config")
			moveSetAside(t, h, ".config/agent-director", "data/ad-config")
			h.Link(target, h.New)
			keptRel, _ := filepath.Rel(h.Home, kept[0])
			before := h.Snapshot()
			installed(t, h)
			after := h.Snapshot()
			for p := range before {
				if p != keptRel && !strings.HasPrefix(p, keptRel+"/") {
					delete(before, p)
				}
			}
			for p := range after {
				if p != keptRel && !strings.HasPrefix(p, keptRel+"/") {
					delete(after, p)
				}
			}
			if d := moveDiff(before, after); d != "" {
				t.Errorf("the kept directory changed:\n%s", d)
			}
			if got := linkTarget(t, h.New); got != target {
				t.Errorf("new dir link = %q, want %q", got, target)
			}
			moveListJSON(t, h, filepath.Join(target, "bin", "agent-director"))
		}},
		{"compat-link-with-an-absolute-target-that-is-absent", func(t *testing.T, h *moveHome) {
			h.Link(h.New, h.Old)
			installed(t, h)
			if got := linkTarget(t, h.Old); got != h.New {
				t.Errorf("compat link = %q, want %q", got, h.New)
			}
			noKept(t, h)
			moveListJSON(t, h, filepath.Join(h.New, "bin", "agent-director"))
		}},
		{"compat-link-with-a-relative-target-that-is-absent", func(t *testing.T, h *moveHome) {
			h.Link(".config/agent-director", h.Old)
			installed(t, h)
			if got := linkTarget(t, h.Old); got != ".config/agent-director" {
				t.Errorf("compat link = %q", got)
			}
			noKept(t, h)
			moveListJSON(t, h, filepath.Join(h.New, "bin", "agent-director"))
		}},
		{"compat-link-with-an-absolute-target-that-is-present", func(t *testing.T, h *moveHome) {
			installed(t, h)
			h.Link(h.New, h.Old)
			installed(t, h)
			if got := linkTarget(t, h.Old); got != h.New {
				t.Errorf("compat link = %q, want %q", got, h.New)
			}
			noKept(t, h)
			moveListJSON(t, h, filepath.Join(h.Old, "bin", "agent-director"))
		}},
		{"compat-link-with-a-relative-target-that-is-present", func(t *testing.T, h *moveHome) {
			installed(t, h)
			h.Link(".config/agent-director/", h.Old)
			installed(t, h)
			if got := linkTarget(t, h.Old); got != ".config/agent-director/" {
				t.Errorf("compat link = %q", got)
			}
			noKept(t, h)
			moveListJSON(t, h, filepath.Join(h.New, "bin", "agent-director"))
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) { row.run(t, newMoveHome(t)) })
	}
}
