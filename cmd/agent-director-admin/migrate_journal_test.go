package main_test

// migrate_journal_test.go covers migrate-journal --check-only through the
// built binary: it makes only the argument checks, before loading any config
// or opening any store, and each refusal's action can be followed literally.
// It also checks every migrate-journal envelope's commit state, through the
// built binary or, where a fault seam is needed, the real administrator run
// as a child of the test binary (migrate_journal_child_test.go).

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/journalfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"golang.org/x/sys/unix"
)

// journalRefusalEnvelope is migrate-journal's ErrJournalMigrationRefused
// envelope, its details kept raw so a test sees which keys are present.
type journalRefusalEnvelope struct {
	ErrName string                     `json:"err_name"`
	Details map[string]json.RawMessage `json:"details"`
}

// journalRefusal decodes stderr's one --check-only envelope
// (assertOnlyEnvelope) and returns details.refusal, failing unless details
// holds only it, cutover "not_committed" and steps_committed false.
func journalRefusal(t *testing.T, stdout, stderr string, code int) (reason, action string) {
	t.Helper()
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrJournalMigrationRefused")
	var env journalRefusalEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("decode %q: %v", stderr, err)
	}
	if _, ok := env.Details["refusal"]; !ok || len(env.Details) != 3 || string(env.Details["cutover"]) != `"not_committed"` || string(env.Details["steps_committed"]) != "false" {
		t.Fatalf("details = %s; want only refusal, cutover not_committed and steps_committed false (no result for --check-only)", stderr)
	}
	var refusal struct {
		Reason    string          `json:"reason"`
		Action    string          `json:"action"`
		Processes json.RawMessage `json:"processes"`
	}
	if err := json.Unmarshal(env.Details["refusal"], &refusal); err != nil || string(refusal.Processes) != "[]" {
		t.Fatalf("details.refusal = %s (%v); want a reason, an action and processes []", env.Details["refusal"], err)
	}
	return refusal.Reason, refusal.Action
}

// checkOnlyArgs returns migrate-journal --check-only's argv with every flag
// valid: one extra legacy root and a backup that does not exist.
func checkOnlyArgs(root, backup string) []string {
	return []string{"migrate-journal", "--check-only", "--maintenance-window", "--roots-complete", "--legacy-root", root, "--backup-dir", backup}
}

// replaceArg returns args with the value after flag set to value.
func replaceArg(args []string, flag, value string) []string {
	out := slices.Clone(args)
	out[slices.Index(out, flag)+1] = value
	return out
}

// dropArg returns args without flag.
func dropArg(args []string, flag string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == flag })
}

// TestMigrateJournalCheckOnly: with valid flags --check-only prints exactly {}
// and exits 0, writing nothing, even over a malformed config.toml and a
// state.db that is not a database; a refusal is one ErrJournalMigrationRefused
// envelope with details.refusal, cutover not_committed and steps_committed
// false, and no details.result.
func TestMigrateJournalCheckOnly(t *testing.T) {
	for _, poisoned := range []bool{false, true} {
		home := t.TempDir()
		if poisoned {
			home = poisonedHome(t)
		}
		base := t.TempDir()
		root, backup := filepath.Join(base, "root"), filepath.Join(base, "backup")
		var before map[string]string
		if poisoned {
			before = dirEntries(t, home)
		}
		stdout, stderr, code := runAdmin(t, home, checkOnlyArgs(root, backup)...)
		if code != 0 || stdout != "{}\n" || stderr != "" {
			t.Errorf("poisoned=%v: exit %d, stdout %q, stderr %q; want 0, {} and nothing", poisoned, code, stdout, stderr)
		}
		if poisoned {
			if after := dirEntries(t, home); !maps.Equal(after, before) {
				t.Errorf("--check-only changed ~/.config/agent-director: %v; want %v", after, before)
			}
		} else if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
			t.Errorf("--check-only made ~/.config: %v", err)
		}
		if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
			t.Errorf("--check-only wrote beside the backup: %v (%v)", entries, err)
		}
	}
	home := poisonedHome(t)
	before := dirEntries(t, home)
	backup := filepath.Join(t.TempDir(), "backup")
	stdout, stderr, code := runAdmin(t, home, dropArg(checkOnlyArgs("/legacy", backup), "--maintenance-window")...)
	if reason, _ := journalRefusal(t, stdout, stderr, code); reason != "maintenance_boundary_required" {
		t.Errorf("reason = %q; want maintenance_boundary_required", reason)
	}
	if after := dirEntries(t, home); !maps.Equal(after, before) {
		t.Errorf("refused --check-only changed ~/.config/agent-director: %v; want %v", after, before)
	}
}

// G11: each migrate-journal argument refusal's action, followed literally in
// agent-director-admin's spelling, gives a --check-only that passes with {}.
func TestAdviceFollow_G11_CheckOnlyArguments(t *testing.T) {
	for _, tc := range []struct {
		reason, action string
		// refuse makes the refused argv (and any file it needs) from the
		// valid one; follow does what the action says.
		refuse, follow func(t *testing.T, args []string, d checkOnlyDirs) []string
		noHome         bool
	}{
		{"maintenance_boundary_required",
			"establish the maintenance window (stop old mutators and prevent new old-binary invocations through committed verification), then pass --maintenance-window (install.sh: --journal-maintenance-window)",
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return dropArg(a, "--maintenance-window") },
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return append(a, "--maintenance-window") }, false},
		{"legacy_roots_not_established",
			"declare every additional legacy input root with --legacy-root, then pass --roots-complete (install.sh: --journal-legacy-root, --journal-roots-complete)",
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return dropArg(a, "--roots-complete") },
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return append(a, "--roots-complete") }, false},
		{"invoking_home_unavailable",
			"run it with HOME set to the invoking user's absolute home directory",
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return a },
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string { return a }, true},
		{"legacy_root_not_absolute",
			"give every --legacy-root (install.sh: --journal-legacy-root) as an absolute path",
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string {
				return replaceArg(a, "--legacy-root", "root")
			},
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--legacy-root", filepath.Join(d.cwd, "root"))
			}, false},
		{"legacy_root_not_clean",
			"give every --legacy-root (install.sh: --journal-legacy-root) in clean form, with no trailing /, no repeated / and no . or .. element",
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--legacy-root", d.root+"//./")
			},
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string {
				return replaceArg(a, "--legacy-root", filepath.Clean(a[slices.Index(a, "--legacy-root")+1]))
			}, false},
		{"absolute_backup_required",
			"pass --backup-dir (install.sh: --journal-backup) a new absolute directory outside every legacy input root",
			func(_ *testing.T, a []string, _ checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", "backup")
			},
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "backup"))
			}, false},
		{"backup_inside_legacy_root",
			"choose a --backup-dir (install.sh: --journal-backup) outside every legacy input root",
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.root, "backup"))
			},
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "backup"))
			}, false},
		{"backup_exists",
			"choose a --backup-dir (install.sh: --journal-backup) that does not exist yet; keep the existing one",
			func(t *testing.T, a []string, d checkOnlyDirs) []string {
				if err := os.Mkdir(filepath.Join(d.cwd, "old-backup"), 0o700); err != nil {
					t.Fatal(err)
				}
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "old-backup"))
			},
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "backup"))
			}, false},
		{"backup_unreadable",
			"choose a --backup-dir (install.sh: --journal-backup) whose parent directories exist and can be searched",
			func(t *testing.T, a []string, d checkOnlyDirs) []string {
				if err := os.WriteFile(filepath.Join(d.cwd, "file"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "file", "backup"))
			},
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "backup"))
			}, false},
		// MI decision Q1 (Task t2.7tz.ab.1h): a parent that does not exist.
		{"backup_unreadable",
			"choose a --backup-dir (install.sh: --journal-backup) whose parent directories exist and can be searched",
			func(_ *testing.T, a []string, d checkOnlyDirs) []string {
				return replaceArg(a, "--backup-dir", filepath.Join(d.cwd, "missing", "backup"))
			},
			func(t *testing.T, a []string, d checkOnlyDirs) []string {
				if err := os.Mkdir(filepath.Join(d.cwd, "missing"), 0o700); err != nil {
					t.Fatal(err)
				}
				return a
			}, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			d := checkOnlyDirs{cwd: cwd, root: filepath.Join(cwd, "legacy")}
			env := append(fakeTmuxEnv(t, home), "HOME="+home)
			refusedEnv := env
			if tc.noHome {
				refusedEnv = fakeTmuxEnv(t, home)
			}
			args := tc.refuse(t, checkOnlyArgs(d.root, filepath.Join(cwd, "backup")), d)
			stdout, stderr, code := runBinIn(t, adminPath, cwd, refusedEnv, args...)
			reason, action := journalRefusal(t, stdout, stderr, code)
			if reason != tc.reason || !strings.Contains(action, tc.action) {
				t.Fatalf("refusal = %s: %q; want %s: %q", reason, action, tc.reason, tc.action)
			}
			stdout, stderr, code = runBinIn(t, adminPath, cwd, env, tc.follow(t, args, d)...)
			if code != 0 || stdout != "{}\n" || stderr != "" {
				t.Fatalf("followed %s: exit %d, stdout %q, stderr %q; want 0 and {}", tc.reason, code, stdout, stderr)
			}
			if tc.reason == "backup_exists" {
				if info, err := os.Stat(filepath.Join(cwd, "old-backup")); err != nil || !info.IsDir() {
					t.Errorf("the existing backup was not kept: %v", err)
				}
			}
		})
	}
}

// checkOnlyDirs is a G11 case's directories: cwd is the run's working
// directory, root its declared legacy root.
type checkOnlyDirs struct{ cwd, root string }

// commitCase is one TestMigrateJournalCommitState row's world: a private HOME
// and a journalfix store, authorized, at db.
type commitCase struct {
	home, dir, db string
	backups       int
}

// lock is the store's migration lock path.
func (c *commitCase) lock() string { return c.db + ".input-migration.lock" }

// args is a valid real-run argv over db with a new backup directory.
func (c *commitCase) args() []string {
	c.backups++
	return []string{"--store-path", c.db, "migrate-journal", "--maintenance-window", "--roots-complete", "--backup-dir", filepath.Join(c.dir, fmt.Sprintf("backup-%d", c.backups))}
}

// env is a run's environment under HOME home ("" for none).
func (c *commitCase) env(t *testing.T, home string) []string {
	env := fakeTmuxEnv(t, c.dir)
	if home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

// holdLock holds the store's migration lock as another administrator does.
func (c *commitCase) holdLock(t *testing.T) {
	t.Helper()
	holder, err := os.OpenFile(c.lock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

// adminChild runs the real administrator as a child of this test binary
// (TestMigrateJournalAdminChild) with seams armed, on args under env; through
// a shell that ignores INT and HUP first when ignoreAtStart. drive, when set,
// runs once the child started, with its process, the reader of its fd 3 and
// the writer of its fd 4. A death by signal fails the test.
func adminChild(t *testing.T, seams string, env, args []string, ignoreAtStart bool, drive func(p *os.Process, ready *bufio.Reader, goAhead *os.File)) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // a failure bound, never a wait
	defer cancel()
	argv := []string{os.Args[0], "-test.run=^TestMigrateJournalAdminChild$"}
	if ignoreAtStart {
		argv = append([]string{"sh", "-c", `trap "" INT HUP; exec "$0" "$@"`}, argv...)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(slices.Clone(env), "AD_ADMIN_CHILD="+seams, "AD_ADMIN_CHILD_ARGS="+string(encoded))
	for _, name := range []string{sandboxguard.EnvVar, sandboxguard.BypassEnvVar} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	toParent, childOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	childIn, fromParent, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer toParent.Close()
	defer fromParent.Close()
	cmd.ExtraFiles = []*os.File{childOut, childIn}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childOut.Close()
	_ = childIn.Close()
	if drive != nil {
		drive(cmd.Process, bufio.NewReader(toParent), fromParent)
	}
	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run the administrator child: %v", err)
		}
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			t.Fatalf("the administrator died of %v (stderr %q)", status.Signal(), stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// awaitReady reads the child's "ready".
func awaitReady(t *testing.T, ready *bufio.Reader) {
	t.Helper()
	if line, err := ready.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("handshake: %q %v", line, err)
	}
}

// signalChild sends sigs to p in order.
func signalChild(t *testing.T, p *os.Process, sigs ...syscall.Signal) {
	t.Helper()
	for _, sig := range sigs {
		if err := syscall.Kill(p.Pid, sig); err != nil {
			t.Fatal(err)
		}
	}
}

// storeVersion reads the user_version of the store at db.
func storeVersion(t *testing.T, db string) (version int) {
	t.Helper()
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// TestMigrateJournalCommitState: SR-8.6 "Commit state". Every migrate-journal
// error envelope carries details.cutover, and details.steps_committed exactly
// when it is not_committed, at every failure point, the real administrator's
// own interrupts included; details.result.authority is there when the attempt
// read it. A refusal before the lock leaves no lock file, and another
// administrator's lock is left as it was.
func TestMigrateJournalCommitState(t *testing.T) {
	type outcome struct {
		errName, reason, cutover, steps string // steps: "true", "false" or "" (absent)
		result                          bool   // details.result present
		authority                       string // its authority
	}
	refused := func(reason, cutover, steps string) outcome {
		return outcome{errName: "ErrJournalMigrationRefused", reason: reason, cutover: cutover, steps: steps, result: true}
	}
	before := func(errName, cutover string) outcome {
		o := outcome{errName: errName, cutover: cutover}
		if cutover == "not_committed" {
			o.steps = "false"
		}
		return o
	}
	interruptBefore := func(sig syscall.Signal) func(*testing.T, *os.Process, *bufio.Reader, *os.File) {
		return func(t *testing.T, p *os.Process, ready *bufio.Reader, _ *os.File) {
			awaitReady(t, ready)
			signalChild(t, p, sig)
		}
	}
	for _, tc := range []struct {
		name      string
		version   int    // the store's: 15 by default
		seams     string // "" runs the built binary
		ignore    bool
		home      func(t *testing.T) string // HOME; a fresh one by default, "" for none
		args      func(t *testing.T, c *commitCase) []string
		setup     func(t *testing.T, c *commitCase)
		drive     func(t *testing.T, p *os.Process, ready *bufio.Reader, goAhead *os.File)
		want      outcome // errName "" is success: exit 0, authority sqlite
		heldLock  bool    // another administrator holds the lock
		storeAt   int     // the store's version after, when set
		badConfig bool
	}{
		{name: "check-only refusal", want: outcome{errName: "ErrJournalMigrationRefused", reason: "maintenance_boundary_required", cutover: "not_committed", steps: "false"},
			args: func(_ *testing.T, c *commitCase) []string {
				return append([]string{"--store-path", c.db}, dropArg(checkOnlyArgs("/legacy", filepath.Join(c.dir, "backup")), "--maintenance-window")...)
			}},
		{name: "first verification refused", seams: "verify-fails-1", want: refused("old_or_unverified_mutators", "not_committed", "false"), storeAt: 15},
		{name: "lock held by another administrator", seams: "pass", want: refused("migration_lock_held", "unknown", ""), heldLock: true},
		{name: "failed backup", seams: "pass", want: refused("backup_failed", "not_committed", "false"), storeAt: 15,
			args: func(t *testing.T, c *commitCase) []string {
				parent := filepath.Join(c.dir, "read-only")
				if err := os.Mkdir(parent, 0o500); err != nil {
					t.Fatal(err)
				}
				return replaceArg(c.args(), "--backup-dir", filepath.Join(parent, "backup"))
			}},
		{name: "earlier step's commit unconfirmed", version: 14, seams: "step-unconfirmed", want: refused("migration_incomplete_or_commit_unknown", "unknown", "")},
		{name: "cutover-step refusal v15", seams: "verify-fails-2", want: refused("old_or_unverified_mutators", "not_committed", "false"), storeAt: 15},
		{name: "cutover-step refusal v14", version: 14, seams: "verify-fails-2", want: refused("old_or_unverified_mutators", "not_committed", "true"), storeAt: 15},
		{name: "unconfirmed cutover commit", seams: "cutover-unconfirmed", want: refused("migration_incomplete_or_commit_unknown", "unknown", "")},
		{name: "unconfirmed cutover commit that called commit", seams: "cutover-unconfirmed-committed", want: refused("migration_incomplete_or_commit_unknown", "unknown", "")},
		{name: "failure after the commit", seams: "verify-fails-3", want: outcome{errName: "ErrJournalMigrationRefused", reason: "committed_verification_incomplete", cutover: "committed", result: true, authority: "sqlite"}},
		{name: "direct retry over a cut-over store", seams: "verify-fails-2", // a current store's call after the open
			want: outcome{errName: "ErrJournalMigrationRefused", reason: "committed_verification_incomplete", cutover: "not_committed", steps: "false", result: true, authority: "sqlite"},
			setup: func(t *testing.T, c *commitCase) {
				if _, _, code := adminChild(t, "pass", c.env(t, c.home), c.args(), false, nil); code != 0 {
					t.Fatalf("the clean attempt exited %d", code)
				}
			}},
		{name: "ErrConfigMalformed with --store-path", badConfig: true, want: before("ErrConfigMalformed", "not_committed")},
		{name: "interrupt INT before the commit", seams: "signal-before", drive: interruptBefore(syscall.SIGINT), want: refused("interrupted", "not_committed", "false"), storeAt: 15},
		{name: "interrupt TERM before the commit", seams: "signal-before", drive: interruptBefore(syscall.SIGTERM), want: refused("interrupted", "not_committed", "false"), storeAt: 15},
		{name: "interrupt HUP before the commit", seams: "signal-before", drive: interruptBefore(syscall.SIGHUP), want: refused("interrupted", "not_committed", "false"), storeAt: 15},
		{name: "interrupt after the commit began", seams: "signal-after",
			drive: func(t *testing.T, p *os.Process, ready *bufio.Reader, goAhead *os.File) {
				awaitReady(t, ready)
				signalChild(t, p, syscall.SIGINT, syscall.SIGTERM) // a second signal must not kill it either
				if _, err := goAhead.WriteString("go\n"); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "INT and HUP ignored at start", seams: "ignored", ignore: true,
			drive: func(t *testing.T, p *os.Process, ready *bufio.Reader, goAhead *os.File) {
				awaitReady(t, ready)
				signalChild(t, p, syscall.SIGINT, syscall.SIGHUP) // discarded: ignored
				if _, err := goAhead.WriteString("go\n"); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "ErrInvalidFlags with --store-path", want: before("ErrInvalidFlags", "not_committed"),
			args: func(_ *testing.T, c *commitCase) []string { return append(c.args(), "--bogus") }},
		{name: "ErrInvalidFlags with --check-only parsed", want: before("ErrInvalidFlags", "not_committed"),
			args: func(*testing.T, *commitCase) []string { return []string{"migrate-journal", "--check-only", "--bogus"} }},
		// --check-only always reports not_committed, parsed or not (CR-4).
		{name: "ErrInvalidFlags with --check-only after the bad flag", want: before("ErrInvalidFlags", "not_committed"),
			args: func(*testing.T, *commitCase) []string { return []string{"migrate-journal", "--bogus", "--check-only"} }},
		{name: "global flag refused by the parse on a --check-only run", want: before("ErrInvalidFlags", "not_committed"),
			args: func(*testing.T, *commitCase) []string {
				return []string{"migrate-journal", "--check-only", "--store-path"}
			}},
		{name: "global flag refused by Apply on a --check-only run", want: before("ErrInvalidFlags", "not_committed"), home: func(*testing.T) string { return "" },
			args: func(*testing.T, *commitCase) []string {
				return []string{"--home", "~/elsewhere", "migrate-journal", "--check-only"}
			}},
		{name: "ErrInvalidFlags with no --store-path", want: before("ErrInvalidFlags", "unknown"),
			args: func(*testing.T, *commitCase) []string { return []string{"migrate-journal", "--bogus"} }},
		{name: "global flag refused by the parse", want: before("ErrInvalidFlags", "unknown"),
			args: func(_ *testing.T, c *commitCase) []string { return append(c.args()[2:], "--store-path") }},
		{name: "global flag refused by Apply", want: before("ErrInvalidFlags", "unknown"), home: func(*testing.T) string { return "" },
			args: func(_ *testing.T, c *commitCase) []string {
				return append([]string{"--home", "~/elsewhere"}, c.args()...)
			}},
		// With HOME unset, the config is read from the literal relative path
		// ~/.config/agent-director/config.toml under the working directory, and
		// an empty db_path makes the store path's resolution need HOME.
		{name: "ErrStoreOpen with no --store-path", want: before("ErrStoreOpen", "unknown"), home: func(*testing.T) string { return "" },
			setup: func(t *testing.T, c *commitCase) {
				config := filepath.Join(c.dir, "~", ".config", "agent-director", "config.toml")
				if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(config, []byte("[store]\ndb_path = \"\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			args: func(_ *testing.T, c *commitCase) []string { return c.args()[2:] }},
		{name: "ErrConfigMalformed while another administrator holds the lock", badConfig: true, heldLock: true, want: before("ErrConfigMalformed", "unknown")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &commitCase{home: t.TempDir(), dir: t.TempDir()}
			if tc.badConfig {
				c.home = poisonedHome(t)
			}
			if tc.home != nil {
				c.home = tc.home(t)
			}
			if tc.version == 0 {
				tc.version = 15
			}
			if err := os.Mkdir(filepath.Join(c.dir, "store"), 0o700); err != nil {
				t.Fatal(err)
			}
			c.db = journalfix.PreCutoverStore(t, filepath.Join(c.dir, "store"), tc.version)
			journalfix.Authorize(t, c.db)
			if tc.setup != nil {
				tc.setup(t, c)
			}
			if tc.heldLock {
				c.holdLock(t)
			}
			var held os.FileInfo
			if tc.heldLock {
				var err error
				if held, err = os.Stat(c.lock()); err != nil {
					t.Fatal(err)
				}
			}
			args := c.args()
			if tc.args != nil {
				args = tc.args(t, c)
			}
			var stdout, stderr string
			var code int
			if tc.seams == "" {
				stdout, stderr, code = runBinIn(t, adminPath, c.dir, c.env(t, c.home), args...)
			} else {
				var drive func(*os.Process, *bufio.Reader, *os.File)
				if tc.drive != nil {
					drive = func(p *os.Process, r *bufio.Reader, w *os.File) { tc.drive(t, p, r, w) }
				}
				stdout, stderr, code = adminChild(t, tc.seams, c.env(t, c.home), args, tc.ignore, drive)
			}
			if tc.want.errName == "" {
				var result struct {
					Authority string `json:"authority"`
				}
				if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || result.Authority != "sqlite" {
					t.Fatalf("exit %d, stdout %q, stderr %q; want 0 with authority sqlite and no envelope", code, stdout, stderr)
				}
			} else {
				assertOnlyEnvelope(t, stdout, stderr, code, tc.want.errName)
				assertCommitDetails(t, stderr, tc.want.reason, tc.want.cutover, tc.want.steps, tc.want.result, tc.want.authority)
			}
			if tc.heldLock {
				if after, err := os.Stat(c.lock()); err != nil || !os.SameFile(held, after) {
					t.Errorf("another administrator's lock file changed: %v", err)
				}
			} else if _, err := os.Lstat(c.lock()); !os.IsNotExist(err) {
				t.Errorf("the run left %s: %v", c.lock(), err)
			}
			if tc.storeAt != 0 {
				if got := storeVersion(t, c.db); got != tc.storeAt {
					t.Errorf("store at v%d; want v%d", got, tc.storeAt)
				}
			}
		})
	}
}

// assertCommitDetails fails unless stderr's envelope details hold exactly
// cutover, steps_committed when steps is set, details.result when result
// (with that authority), and details.refusal with reason when it is set.
func assertCommitDetails(t *testing.T, stderr, reason, cutover, steps string, result bool, authority string) {
	t.Helper()
	var env journalRefusalEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("decode %q: %v", stderr, err)
	}
	want := map[string]bool{"cutover": true, "steps_committed": steps != "", "result": result, "refusal": reason != ""}
	keys := 0
	for key, present := range want {
		if _, ok := env.Details[key]; ok != present {
			t.Errorf("details.%s present = %v; want %v (%s)", key, ok, present, stderr)
		}
		if present {
			keys++
		}
	}
	if len(env.Details) != keys {
		t.Errorf("details = %s; want only %v", stderr, want)
	}
	if got := string(env.Details["cutover"]); got != `"`+cutover+`"` {
		t.Errorf("details.cutover = %s; want %q", got, cutover)
	}
	if steps != "" && string(env.Details["steps_committed"]) != steps {
		t.Errorf("details.steps_committed = %s; want %s", env.Details["steps_committed"], steps)
	}
	if result {
		var r struct {
			Authority string `json:"authority"`
		}
		if err := json.Unmarshal(env.Details["result"], &r); err != nil || r.Authority != authority {
			t.Errorf("details.result = %s (%v); want authority %q", env.Details["result"], err, authority)
		}
	}
	if reason != "" {
		var r struct {
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(env.Details["refusal"], &r); err != nil || r.Reason != reason {
			t.Errorf("details.refusal = %s (%v); want reason %s", env.Details["refusal"], err, reason)
		}
	}
}
