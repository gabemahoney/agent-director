package installsh_test

// Pre-flight's native-input cutover decision (SR-2.14 "Pre-flight", SR-2.16
// "Outcomes"), from genuine previous installs (release_fixture_test.go). A
// crossing upgrade runs the candidate administrator's migrate-journal
// --check-only before the swap; every refusal here leaves the home byte for
// byte as it was. The outcome helpers below (preflightEnvelopes,
// assertOutcomeEnvelope, assertRefusedUnchanged) are for every case that
// checks an install.sh outcome envelope.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// candidateAction is the action of install.sh's own envelope when
// --check-only gives an unexpected answer.
const candidateAction = "run this release's agent-director-admin as the candidate (the same release as agent-director), then re-run this install; report it if it persists"

// preflightEnvelopes returns res's stderr lines that are JSON error envelopes
// (an object with a string err_name), in order, failing unless the last line
// is the only one starting "install.sh: err_name=" and the last envelope's
// err_name equals it.
func preflightEnvelopes(t *testing.T, res moveResult, cause string) []string {
	t.Helper()
	var envelopes []string
	lines := strings.Split(strings.TrimRight(res.Stderr, "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "install.sh: err_name=") && i != len(lines)-1 {
			t.Errorf("stderr line %d is a second cause line: %q", i, line)
		}
		var env struct {
			ErrName *string `json:"err_name"`
		}
		if json.Unmarshal([]byte(line), &env) == nil && env.ErrName != nil {
			envelopes = append(envelopes, line)
		}
	}
	if want := "install.sh: err_name=" + cause; res.Code != 5 || res.Last() != want {
		t.Fatalf("exit %d, last line %q; want 5 and %q\nstdout:\n%s\nstderr:\n%s", res.Code, res.Last(), want, res.Stdout, res.Stderr)
	}
	if len(envelopes) == 0 {
		t.Fatalf("no envelope above the cause line:\n%s", res.Stderr)
	}
	var last struct {
		ErrName string `json:"err_name"`
	}
	_ = json.Unmarshal([]byte(envelopes[len(envelopes)-1]), &last)
	if last.ErrName != cause {
		t.Errorf("last envelope's err_name %q differs from the cause %s", last.ErrName, cause)
	}
	return envelopes
}

// assertOutcomeEnvelope fails unless line is exactly install.sh's own outcome
// envelope (ad_outcome_exit) with these fields and no others besides extra.
func assertOutcomeEnvelope(t *testing.T, line, errName, reason, action, cutover string, extra map[string]any) {
	t.Helper()
	details := map[string]any{"refusal": map[string]any{"reason": reason, "processes": []any{}, "action": action}, "cutover": cutover}
	for k, v := range extra {
		details[k] = v
	}
	want := map[string]any{"err_name": errName, "err_description": "install.sh: " + reason + ": " + action, "details": details}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil || !reflect.DeepEqual(got, want) {
		wantLine, _ := json.Marshal(want)
		t.Errorf("outcome envelope:\n got %s\nwant %s", line, wantLine)
	}
}

// assertRefusedUnchanged fails unless h's home is physically as before (the
// installed pair, the managed PATH link, the store's files and any sentinel
// included) and install.sh left nothing in TMPDIR.
func assertRefusedUnchanged(t *testing.T, h *moveHome, before moveTree) {
	t.Helper()
	if d := moveDiff(before, h.PhysicalSnapshot()); d != "" {
		t.Errorf("a pre-flight refusal changed the home:\n%s", d)
	}
	if got := h.tempEntries(); len(got) != 0 {
		t.Errorf("install.sh left temp entries: %v", got)
	}
}

// crossingCase is a home holding a genuine v0.17.3 install (store v15), with
// a private extra legacy root and a file to put a backup under.
type crossingCase struct {
	h       *moveHome
	release releaseBuild
	prev    string // v0.17.3's installed agent-director
	root    string // a legacy input root, 0700
	file    string // a regular file
	db      string
	backups int
}

func newCrossingCase(t *testing.T) *crossingCase {
	t.Helper()
	r := genuineRelease(t, "v0.17.3")
	h := newMoveHome(t)
	h.Flags = []string{"--no-hooks"} // the managed PATH link is part of what must not change
	c := &crossingCase{h: h, release: r, root: filepath.Join(h.Root, "legacy-input"), file: filepath.Join(h.Root, "file"), db: filepath.Join(h.New, "state.db")}
	c.prev = h.InstallRelease(r)
	if v := moveUserVersion(t, h, c.db); v != "15" {
		t.Fatalf("v0.17.3 made a store at v%s; want v15", v)
	}
	if err := os.Mkdir(c.root, 0o700); err != nil {
		t.Fatal(err)
	}
	h.Write(c.file, "not a directory\n", 0o600)
	return c
}

// newBackup returns a new absolute backup directory path outside every root.
func (c *crossingCase) newBackup() string {
	c.backups++
	return filepath.Join(c.h.Root, "backup-"+strconv.Itoa(c.backups))
}

// valid is a crossing run's journal flags that --check-only accepts.
func (c *crossingCase) valid() []string {
	return []string{"--journal-maintenance-window", "--journal-roots-complete", "--journal-backup", c.newBackup()}
}

// adminArgs maps install.sh's journal flags to migrate-journal's, as
// ad_journal_args does.
func adminArgs(flags []string) []string {
	out := []string{"migrate-journal", "--check-only"}
	backup, roots := "", []string{}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--journal-maintenance-window":
			out = append(out, "--maintenance-window")
		case "--journal-roots-complete":
			out = append(out, "--roots-complete")
		case "--journal-backup":
			i++
			backup = flags[i]
		case "--journal-legacy-root":
			i++
			roots = append(roots, flags[i])
		}
	}
	out = append(out, "--backup-dir", backup)
	for _, r := range roots {
		out = append(out, "--legacy-root", r)
	}
	return out
}

// setFlag returns flags with flag's value set to value (added when absent).
func setFlag(flags []string, flag, value string) []string {
	out := append([]string(nil), flags...)
	for i := range out {
		if out[i] == flag {
			out[i+1] = value
			return out
		}
	}
	return append(out, flag, value)
}

// mapRoots returns flags with every --journal-legacy-root value mapped by f.
func mapRoots(flags []string, f func(string) string) []string {
	out := append([]string(nil), flags...)
	for i := range out {
		if out[i] == "--journal-legacy-root" {
			out[i+1] = f(out[i+1])
		}
	}
	return out
}

// follow does what a refusal's action says, in install.sh's spelling.
func (c *crossingCase) follow(t *testing.T, reason, action string, flags []string) []string {
	t.Helper()
	if action == candidateAction {
		c.h.Pair = c.h.S.Current
		delete(c.h.Env, "SCHED_TEST_DELEGATE")
		return flags
	}
	switch reason {
	case "maintenance_boundary_required":
		return append(append([]string(nil), flags...), "--journal-maintenance-window")
	case "legacy_roots_not_established":
		// This home has no legacy root beyond the declared ones.
		return append(append([]string(nil), flags...), "--journal-roots-complete")
	case "legacy_root_not_absolute":
		return mapRoots(flags, func(r string) string {
			if filepath.IsAbs(r) {
				return r
			}
			return filepath.Join(c.h.Cwd, r)
		})
	case "legacy_root_not_clean":
		return mapRoots(flags, filepath.Clean)
	case "absolute_backup_required", "backup_inside_legacy_root", "backup_exists", "backup_unreadable":
		return setFlag(flags, "--journal-backup", c.newBackup())
	}
	t.Fatalf("no follow for %s: %q", reason, action)
	return nil
}

// refusalOf returns the reason and action of a refusal's last envelope.
func refusalOf(t *testing.T, line string) (reason, action string) {
	t.Helper()
	var env struct {
		Details struct {
			Refusal struct{ Reason, Action string } `json:"refusal"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		t.Fatalf("envelope %q: %v", line, err)
	}
	return env.Details.Refusal.Reason, env.Details.Refusal.Action
}

// checkOnlyRefusal checks res is a pre-flight refusal relaying exactly the
// envelope the candidate administrator's --check-only writes for flags, with
// the home unchanged and v0.17.3's list still working; it returns the
// refusal's reason and action.
func (c *crossingCase) checkOnlyRefusal(t *testing.T, res moveResult, flags []string, before moveTree) (string, string) {
	t.Helper()
	reason, action := assertCheckOnlyRelayed(t, c.h, res, flags)
	assertRefusedUnchanged(t, c.h, before)
	if got := c.h.Run(c.prev, "list"); got.Code != 0 {
		t.Errorf("v0.17.3's list after the refusal: exit %d: %s", got.Code, got.Stderr)
	}
	return reason, action
}

// assertCheckOnlyRelayed checks res is pre-flight's refusal of the journal
// flags in flags: its advice lines, and exactly the one envelope h's
// candidate administrator's --check-only writes for them. It returns the
// refusal's reason and action.
func assertCheckOnlyRelayed(t *testing.T, h *moveHome, res moveResult, flags []string) (string, string) {
	t.Helper()
	envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
	ref := h.Run(h.Pair.Admin, adminArgs(flags)...)
	if want := strings.TrimRight(ref.Stderr, "\n"); ref.Code == 0 || len(envs) != 1 || envs[0] != want {
		t.Errorf("relayed envelopes %q; want exactly --check-only's %q", envs, want)
	}
	reason, action := refusalOf(t, envs[len(envs)-1])
	for _, line := range []string{"install.sh: pre-flight refused the journal flags (agent-director-admin migrate-journal --check-only)",
		"  Nothing was installed or changed.", "  To fix: " + action,
		"  Then re-run this release's installer, with a new --journal-backup directory."} {
		if !strings.Contains(res.Stderr, line+"\n") {
			t.Errorf("stderr lacks %q:\n%s", line, res.Stderr)
		}
	}
	return reason, action
}

// assertCrossed fails unless res is a passing crossing upgrade: pre-flight's
// cutover line, step 4's migrate-journal result, the store at this release's
// version with native-input authority recorded, and backup complete.
func (c *crossingCase) assertCrossed(t *testing.T, res moveResult, backup string) {
	t.Helper()
	h := c.h
	if res.Code != 0 {
		t.Fatalf("install: exit %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	for _, s := range []string{"  cutover : " + c.db + " at v15 crosses the native-input cutover\n", "  journal : "} {
		if !strings.Contains(res.Stdout, s) {
			t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
		}
	}
	if v, want := moveUserVersion(t, h, c.db), moveCurrentVersion(t, h); v != want {
		t.Errorf("store at v%s; want v%s", v, want)
	}
	if got := h.Sqlite(c.db, "SELECT count(*) FROM store_meta WHERE key='native_input_authority' AND value LIKE 'sqlite-v1:%';", "-readonly"); got != "1\n" {
		t.Errorf("native-input authority not recorded: %q", got)
	}
	for _, f := range []string{"COMPLETE", "database", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(backup, f)); err != nil {
			t.Errorf("backup %s: %v", f, err)
		}
	}
	if got := h.Run(filepath.Join(h.New, "bin", "agent-director"), "list"); got.Code != 0 {
		t.Errorf("installed list: exit %d: %s", got.Code, got.Stderr)
	}
}

// journalBackup is flags' --journal-backup value.
func journalBackup(flags []string) string {
	for i := range flags {
		if flags[i] == "--journal-backup" {
			return flags[i+1]
		}
	}
	return ""
}

// TestAdviceFollow_J35_CheckOnlyRefusals: from a genuine v0.17.3 install,
// each --check-only refusal the installer can reach (and an unexpected
// --check-only answer) refuses before the swap with nothing changed; its
// advice, followed literally in install.sh's spelling, gives a passing
// crossing upgrade. J35: "To fix: <the refusal's action>" / "Then re-run this
// release's installer, with a new --journal-backup directory."
//
// invoking_home_unavailable is unreachable here: install.sh resolves the
// install from an absolute HOME and passes that HOME to --check-only.
func TestAdviceFollow_J35_CheckOnlyRefusals(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
	}
	for _, tc := range []struct {
		name, reason string
		// flags gives the run's journal flags; it may change the case (a
		// stand-in candidate, an existing backup).
		flags  func(t *testing.T, c *crossingCase) []string
		darwin bool
	}{
		{"no journal flags", "maintenance_boundary_required", func(*testing.T, *crossingCase) []string { return nil }, false},
		{"window only", "legacy_roots_not_established", func(*testing.T, *crossingCase) []string { return []string{"--journal-maintenance-window"} }, false},
		{"no backup", "absolute_backup_required", func(*testing.T, *crossingCase) []string {
			return []string{"--journal-maintenance-window", "--journal-roots-complete"}
		}, false},
		{"relative legacy root", "legacy_root_not_absolute", func(t *testing.T, c *crossingCase) []string {
			return append(c.valid(), "--journal-legacy-root", "../legacy-input")
		}, false},
		{"trailing slash legacy root", "legacy_root_not_clean", func(t *testing.T, c *crossingCase) []string {
			return append(c.valid(), "--journal-legacy-root", c.root+"/")
		}, false},
		{"backup inside a legacy root", "backup_inside_legacy_root", func(t *testing.T, c *crossingCase) []string {
			return append(setFlag(c.valid(), "--journal-backup", filepath.Join(c.root, "backup")), "--journal-legacy-root", c.root)
		}, false},
		{"existing backup", "backup_exists", func(t *testing.T, c *crossingCase) []string {
			flags := c.valid()
			if err := os.Mkdir(journalBackup(flags), 0o700); err != nil {
				t.Fatal(err)
			}
			return flags
		}, false},
		{"backup under a file", "backup_unreadable", func(t *testing.T, c *crossingCase) []string {
			return setFlag(c.valid(), "--journal-backup", filepath.Join(c.file, "backup"))
		}, false},
		{"no envelope from --check-only", "check_only_unestablished", func(t *testing.T, c *crossingCase) []string {
			c.standInCandidate("echo 'check-only: no answer'; exit 1")
			return c.valid()
		}, false},
		{"another err_name from --check-only", "", func(t *testing.T, c *crossingCase) []string {
			c.standInCandidate("exec " + moveQuote(c.release.Pair.Admin) + ` "$@"`)
			return c.valid()
		}, false},
		{"darwin trailing slash legacy root", "legacy_root_not_clean", func(t *testing.T, c *crossingCase) []string {
			c.standInDarwin()
			return append(c.valid(), "--journal-legacy-root", c.root+"/")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCrossingCase(t)
			h := c.h
			flags := tc.flags(t, c)
			for attempt := 0; ; attempt++ {
				before := h.PhysicalSnapshot()
				backup := journalBackup(flags)
				_, statErr := os.Lstat(backup)
				res := h.Install(flags...)
				if res.Code == 0 {
					if attempt == 0 {
						t.Fatalf("the first run passed; want a %s refusal", tc.reason)
					}
					c.assertCrossed(t, res, journalBackup(flags))
					break
				}
				var reason, action string
				if attempt == 0 && (tc.reason == "check_only_unestablished" || tc.reason == "") {
					reason, action = c.candidateRefusal(t, res, flags, before, tc.reason)
				} else {
					reason, action = c.checkOnlyRefusal(t, res, flags, before)
				}
				if _, err := os.Lstat(backup); backup != "" && statErr != nil && err == nil {
					t.Errorf("the refusal made the backup %s", backup)
				}
				if attempt == 0 && tc.reason != "" && reason != tc.reason {
					t.Fatalf("first refusal %s; want %s", reason, tc.reason)
				}
				if attempt == 3 || t.Failed() {
					t.Fatalf("still refused after %d follows (%s)", attempt, reason)
				}
				flags = c.follow(t, reason, action, flags)
				if reason == "backup_exists" {
					defer func() {
						if info, err := os.Stat(backup); err != nil || !info.IsDir() {
							t.Errorf("the existing backup %s was not kept: %v", backup, err)
						}
					}()
				}
			}
			if tc.darwin {
				c.assertDarwinOnly(t)
			}
		})
	}
	// With the old-layout move due, the decision reads the old directory's
	// store and refuses before the move: ~/.agent-director is untouched and no
	// ~/.config/agent-director exists. Followed, the install moves and crosses.
	t.Run("move due no backup", func(t *testing.T) {
		h := newMoveHome(t)
		h.Flags = append(h.Flags, "--journal-maintenance-window", "--journal-roots-complete") // authorizeJournalMigration keeps these
		h.BuildOld()
		before := h.PhysicalSnapshot()
		res := h.Install()
		reason, action := assertCheckOnlyRelayed(t, h, res, h.Flags)
		if reason != "absolute_backup_required" {
			t.Fatalf("refusal %s; want absolute_backup_required", reason)
		}
		assertRefusedUnchanged(t, h, before)
		if _, err := os.Lstat(h.New); err == nil {
			t.Errorf("the refusal made %s", h.New)
		}
		if !strings.Contains(action, "--journal-backup") {
			t.Fatalf("action %q does not name --journal-backup", action)
		}
		backup := filepath.Join(h.Root, "backup-1")
		moved := h.Snapshot()
		res = moveMustInstall(t, h, "--journal-backup", backup)
		moveCheckMoved(t, h, moved)
		db := filepath.Join(h.New, "state.db")
		if v, want := moveUserVersion(t, h, db), moveCurrentVersion(t, h); v != want {
			t.Errorf("store at v%s; want v%s", v, want)
		}
		if got := h.Sqlite(db, "SELECT count(*) FROM store_meta WHERE key='native_input_authority' AND value LIKE 'sqlite-v1:%';", "-readonly"); got != "1\n" {
			t.Errorf("native-input authority not recorded: %q", got)
		}
		if _, err := os.Stat(filepath.Join(backup, "COMPLETE")); err != nil {
			t.Errorf("backup: %v", err)
		}
		if !strings.Contains(res.Stdout, "  journal : ") {
			t.Errorf("stdout lacks step 4's journal line:\n%s", res.Stdout)
		}
	})
}

// standInCandidate makes the candidate administrator a stand-in (the move
// fixture's stamped standin, delegating) that runs script for a --check-only
// call and this tree's administrator for every other.
func (c *crossingCase) standInCandidate(script string) {
	h := c.h
	delegate := h.WriteTool("check-only-candidate", `for a in "$@"; do if [[ "$a" == --check-only ]]; then `+script+`; fi; done
exec `+moveQuote(h.S.Current.Admin)+` "$@"`)
	h.Pair = movePair{h.S.Current.Main, h.S.ScheduleProbe.Main}
	h.Env["SCHED_TEST_DELEGATE"] = delegate
}

// candidateRefusal checks res is install.sh's own pre-flight envelope for an
// unexpected --check-only answer: the captured output relayed (an envelope
// line unchanged, any other line indented), then the outcome envelope, with
// nothing changed. reason "" means the relayed envelope's err_name.
func (c *crossingCase) candidateRefusal(t *testing.T, res moveResult, flags []string, before moveTree, reason string) (string, string) {
	t.Helper()
	h := c.h
	envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
	if reason == "" {
		ref := h.Run(c.release.Pair.Admin, adminArgs(flags)...)
		relayed := strings.TrimRight(ref.Stderr, "\n")
		var name struct {
			ErrName string `json:"err_name"`
		}
		if err := json.Unmarshal([]byte(relayed), &name); err != nil || name.ErrName == "" || name.ErrName == "ErrJournalMigrationRefused" {
			t.Fatalf("v0.17.3's --check-only answer %q (%v); want another error's envelope", relayed, err)
		}
		reason = name.ErrName
		if len(envs) != 2 || envs[0] != relayed {
			t.Errorf("envelopes %q; want the relayed %q, then install.sh's own", envs, relayed)
		}
	} else if len(envs) != 1 || !strings.Contains(res.Stderr, "\n  check-only: no answer\n") {
		t.Errorf("want the captured output indented and one envelope; envelopes %q, stderr:\n%s", envs, res.Stderr)
	}
	assertOutcomeEnvelope(t, envs[len(envs)-1], "ErrJournalMigrationRefused", reason, candidateAction, "not_committed", nil)
	for _, line := range []string{"install.sh: pre-flight could not check the journal flags: agent-director-admin migrate-journal --check-only gave an unexpected result (status 1)",
		"  Nothing was installed or changed."} {
		if !strings.Contains(res.Stderr, line+"\n") {
			t.Errorf("stderr lacks %q:\n%s", line, res.Stderr)
		}
	}
	assertRefusedUnchanged(t, h, before)
	if got := h.Run(c.prev, "list"); got.Code != 0 {
		t.Errorf("v0.17.3's list after the refusal: exit %d: %s", got.Code, got.Stderr)
	}
	return reason, candidateAction
}

// darwinOnlyTools are macOS-only commands; standInDarwin makes each log its
// call and fail, so a run that reached one shows it.
var darwinOnlyTools = []string{"dscl", "sw_vers", "launchctl", "xattr", "codesign", "ditto", "plutil", "defaults", "scutil", "security", "diskutil", "pkgutil"}

// standInDarwin runs the case's installs through install.sh's Darwin
// branch: uname answers Darwin arm64, file describes every binary as a
// Mach-O arm64 executable, and each darwinOnlyTools stand-in logs its call.
func (c *crossingCase) standInDarwin() {
	h := c.h
	branch := moveQuote(filepath.Join(h.Root, "darwin-branch.log"))
	h.WriteTool("uname", `echo "uname $*" >>`+branch+`; case "${1:-}" in -m) echo arm64 ;; *) echo Darwin ;; esac`)
	h.WriteTool("file", `echo "file" >>`+branch+`; echo "Mach-O 64-bit executable arm64"`)
	for _, name := range darwinOnlyTools {
		h.WriteTool(name, `echo "$0 $*" >>`+moveQuote(filepath.Join(h.Root, "darwin-tools.log"))+`; exit 1`)
	}
}

// assertDarwinOnly fails unless the run went through the Darwin branch's
// stand-ins only: no macOS-only tool was called.
func (c *crossingCase) assertDarwinOnly(t *testing.T) {
	t.Helper()
	branch, _ := os.ReadFile(filepath.Join(c.h.Root, "darwin-branch.log"))
	if !strings.Contains(string(branch), "uname -s\n") || !strings.Contains(string(branch), "file\n") {
		t.Errorf("the runs did not take the Darwin branch through the uname and file stand-ins: %q", branch)
	}
	if b, err := os.ReadFile(filepath.Join(c.h.Root, "darwin-tools.log")); err == nil {
		t.Errorf("the Darwin branch called macOS-only tools:\n%s", b)
	}
}

// TestCrossingPreflightStoreState: a store past the cutover (v16, below this
// release's version) whose native-input authority cannot be read, or is not
// recorded, refuses in pre-flight with install.sh's own envelope, changing
// nothing; v0.17.3's list refuses it after the run as before. A v0 store does
// not cross: an install with no journal flags passes and takes list.
//
// No released version makes a v16 to v19 store: these are the genuine
// v0.17.3 store raised by hand, in the shapes SR-8.6 describes.
func TestCrossingPreflightStoreState(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
	}
	for _, tc := range []struct{ reason, sql, what, inspect string }{
		{"cutover_state_unreadable", "DROP TABLE store_meta; PRAGMA user_version=16;", "unreadable",
			"state.db's store_meta table and its native_input_authority row"},
		{"native_input_authority_missing", "PRAGMA user_version=16;", "not recorded",
			"the native_input_authority row of state.db's store_meta table"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			c := newCrossingCase(t)
			h := c.h
			h.Sqlite(c.db, tc.sql)
			prevErr := func() string {
				got := h.Run(c.prev, "list")
				var env struct {
					ErrName string `json:"err_name"`
				}
				if err := json.Unmarshal([]byte(got.Stderr), &env); err != nil || got.Code == 0 || env.ErrName == "" {
					t.Fatalf("v0.17.3's list on the v16 store: exit %d, stderr %q; want a refusal", got.Code, got.Stderr)
				}
				return env.ErrName
			}
			wantErr := prevErr()
			before := h.PhysicalSnapshot()
			res := h.Install(c.valid()...)
			envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
			action := "inspect " + tc.inspect + ", or restore state.db's pre-upgrade copy, as docs/operator-actions.md \"Upgrading the native-input journal\" says, then re-run this install"
			if len(envs) != 1 {
				t.Errorf("envelopes %q; want install.sh's own only", envs)
			}
			assertOutcomeEnvelope(t, envs[len(envs)-1], "ErrJournalMigrationRefused", tc.reason, action, "not_committed", nil)
			for _, line := range []string{"install.sh: state.db at v16 is past the native-input cutover, but its native-input authority is " + tc.what,
				"  state.db: " + c.db, "  Nothing was installed or changed."} {
				if !strings.Contains(res.Stderr, line+"\n") {
					t.Errorf("stderr lacks %q:\n%s", line, res.Stderr)
				}
			}
			assertRefusedUnchanged(t, h, before)
			if got := prevErr(); got != wantErr {
				t.Errorf("v0.17.3's list refuses with %s after the run; %s before it", got, wantErr)
			}
		})
	}
	t.Run("v0 store", func(t *testing.T) {
		h := newMoveHome(t)
		db := filepath.Join(h.New, "state.db")
		h.Write(db, "", 0o600)
		if v := moveUserVersion(t, h, db); v != "0" {
			t.Fatalf("store at v%s; want v0", v)
		}
		res := moveMustInstall(t, h)
		for _, s := range []string{"  cutover : ", "  journal : "} {
			if strings.Contains(res.Stdout, s) {
				t.Errorf("a v0 store's install printed %q, as a crossing one does:\n%s", s, res.Stdout)
			}
		}
		if v, want := moveUserVersion(t, h, db), moveCurrentVersion(t, h); v != want {
			t.Errorf("store at v%s after list; want v%s", v, want)
		}
	})
}
