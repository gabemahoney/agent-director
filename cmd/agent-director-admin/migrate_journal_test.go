package main_test

// migrate_journal_test.go covers migrate-journal --check-only through the
// built binary: it makes only the argument checks, before loading any config
// or opening any store, and each refusal's action can be followed literally.

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// journalRefusalEnvelope is migrate-journal's ErrJournalMigrationRefused
// envelope, its details kept raw so a test sees which keys are present.
type journalRefusalEnvelope struct {
	ErrName string                     `json:"err_name"`
	Details map[string]json.RawMessage `json:"details"`
}

// journalRefusal decodes stderr's one envelope (assertOnlyEnvelope) and
// returns details.refusal, failing if details holds anything else.
func journalRefusal(t *testing.T, stdout, stderr string, code int) (reason, action string) {
	t.Helper()
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrJournalMigrationRefused")
	var env journalRefusalEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("decode %q: %v", stderr, err)
	}
	if _, ok := env.Details["refusal"]; !ok || len(env.Details) != 1 {
		t.Fatalf("details = %s; want only refusal (no result for --check-only)", stderr)
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
// envelope with details.refusal and no details.result.
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
