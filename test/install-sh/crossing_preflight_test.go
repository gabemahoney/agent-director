package installsh_test

// Pre-flight's native-input cutover decision (SR-2.14 "Pre-flight", SR-2.16
// "Outcomes"), from genuine previous installs (release_fixture_test.go). A
// crossing upgrade runs the candidate administrator's migrate-journal
// --check-only before the swap, then the restore preconditions (the backup
// outside AD's directories, the predates probe, the restore's copies); every
// refusal here leaves the home byte for byte as it was. The outcome helpers
// below (preflightEnvelopes, assertOutcomeEnvelope, assertOwnRefusal,
// assertRefusedUnchanged) are for every case that checks an install.sh
// outcome envelope.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
// included) and install.sh left nothing in TMPDIR. touched names directories
// (relative to HOME) whose size and mtime are left out, for a refusal that
// made and removed copies in them; every entry in them is still compared.
func assertRefusedUnchanged(t *testing.T, h *moveHome, before moveTree, touched ...string) {
	t.Helper()
	want, got := moveTree{}, h.PhysicalSnapshot()
	for p, v := range before {
		want[p] = v
	}
	for _, rel := range touched {
		for _, tr := range []moveTree{want, got} {
			if v, ok := tr[rel]; ok {
				tr[rel] = physicalTimes.ReplaceAllString(v, " ")
			}
		}
	}
	if d := moveDiff(want, got); d != "" {
		t.Errorf("a pre-flight refusal changed the home:\n%s", d)
	}
	if got := h.tempEntries(); len(got) != 0 {
		t.Errorf("install.sh left temp entries: %v", got)
	}
}

// physicalTimes is the size and mtime in a PhysicalSnapshot entry.
var physicalTimes = regexp.MustCompile(` size=\d+ mtime=-?\d+ `)

// assertOwnRefusal checks res is pre-flight's refusal with install.sh's own
// reason and action: exit 5, its outcome envelope last, the cause line, and
// each of lines on stderr before "  Nothing was installed or changed.".
func assertOwnRefusal(t *testing.T, res moveResult, reason, action string, lines ...string) {
	t.Helper()
	envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
	assertOutcomeEnvelope(t, envs[len(envs)-1], "ErrJournalMigrationRefused", reason, action, "not_committed", nil)
	for _, line := range append(lines, "  Nothing was installed or changed.") {
		if !strings.Contains(res.Stderr, line+"\n") {
			t.Errorf("stderr lacks %q:\n%s", line, res.Stderr)
		}
	}
}

// crossingCase is a home holding a genuine v0.17.3 install (store v15), or
// the release newCrossingCaseOf names, with a private extra legacy root and a
// file to put a backup under.
type crossingCase struct {
	h       *moveHome
	release releaseBuild
	prev    string // the release's installed agent-director
	version string // its store's user_version
	root    string // a legacy input root, 0700
	file    string // a regular file
	db      string
	backups int
}

// newCrossingCase installs v0.17.3 after each setup has changed the home (its
// flags, say).
func newCrossingCase(t *testing.T, setup ...func(h *moveHome)) *crossingCase {
	t.Helper()
	return newCrossingCaseOf(t, "v0.17.3", setup...)
}

// crossingStoreVersions is the store version each release a crossingCase
// installs makes.
var crossingStoreVersions = map[string]string{"v0.16.0": "14", "v0.17.3": "15"}

// newCrossingCaseOf is newCrossingCase from tag's genuine install.
func newCrossingCaseOf(t *testing.T, tag string, setup ...func(h *moveHome)) *crossingCase {
	t.Helper()
	r := genuineRelease(t, tag)
	h := newMoveHome(t)
	h.Flags = []string{"--no-hooks"} // the managed PATH link is part of what must not change
	for _, f := range setup {
		f(h)
	}
	c := &crossingCase{h: h, release: r, version: crossingStoreVersions[tag], root: filepath.Join(h.Root, "legacy-input"), file: filepath.Join(h.Root, "file"), db: filepath.Join(h.New, "state.db")}
	c.prev = h.InstallRelease(r)
	if v := moveUserVersion(t, h, c.db); v != c.version {
		t.Fatalf("%s made a store at v%s; want v%s", tag, v, c.version)
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
	for _, s := range []string{"  cutover : " + c.db + " at v" + c.version + " crosses the native-input cutover\n", "  journal : "} {
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
			standInDarwin(c.h)
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
				assertDarwinOnly(t, c.h)
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
		assertMovedCrossed(t, h, moved, h.Install("--journal-backup", backup), backup)
	})
}

// assertMovedCrossed fails unless res is a passing upgrade that moved the old
// layout (moveCheckMoved against moved, the snapshot before the run, unless
// it is nil) and crossed: the store at this release's version with
// native-input authority recorded, backup complete, and step 4's journal
// line.
func assertMovedCrossed(t *testing.T, h *moveHome, moved moveTree, res moveResult, backup string) {
	t.Helper()
	if res.Code != 0 {
		t.Fatalf("install: exit %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if moved != nil {
		moveCheckMoved(t, h, moved)
	} else if _, err := os.Lstat(h.Old); err == nil {
		t.Errorf("%s was not moved", h.Old)
	}
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

// standInDarwin runs h's installs through install.sh's Darwin branch: uname
// answers Darwin arm64, file describes every binary as a Mach-O arm64
// executable, and each darwinOnlyTools stand-in logs its call.
func standInDarwin(h *moveHome) {
	branch := moveQuote(filepath.Join(h.Root, "darwin-branch.log"))
	h.WriteTool("uname", `echo "uname $*" >>`+branch+`; case "${1:-}" in -m) echo arm64 ;; *) echo Darwin ;; esac`)
	h.WriteTool("file", `echo "file" >>`+branch+`; echo "Mach-O 64-bit executable arm64"`)
	for _, name := range darwinOnlyTools {
		h.WriteTool(name, `echo "$0 $*" >>`+moveQuote(filepath.Join(h.Root, "darwin-tools.log"))+`; exit 1`)
	}
}

// assertDarwinOnly fails unless h's runs went through the Darwin branch's
// stand-ins only: no macOS-only tool was called.
func assertDarwinOnly(t *testing.T, h *moveHome) {
	t.Helper()
	branch, _ := os.ReadFile(filepath.Join(h.Root, "darwin-branch.log"))
	if !strings.Contains(string(branch), "uname -s\n") || !strings.Contains(string(branch), "file\n") {
		t.Errorf("the runs did not take the Darwin branch through the uname and file stand-ins: %q", branch)
	}
	if b, err := os.ReadFile(filepath.Join(h.Root, "darwin-tools.log")); err == nil {
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
//
// J43 runs the two v16 rows (crossingV16Rows), continuing each with the
// documented inspection and way back.
func TestCrossingPreflightStoreState(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
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

// crossingV16Row is one v16 store pre-flight refuses: the SQL that raises
// v0.17.3's store to it, and the refusal's wording.
type crossingV16Row struct{ reason, sql, what, inspect string }

var crossingV16Rows = []crossingV16Row{
	{"cutover_state_unreadable", "DROP TABLE store_meta; PRAGMA user_version=16;", "unreadable",
		"state.db's store_meta table and its native_input_authority row"},
	{"native_input_authority_missing", "PRAGMA user_version=16;", "not recorded",
		"the native_input_authority row of state.db's store_meta table"},
}

// crossingV16Refused raises a genuine v0.17.3 store to tc's v16 shape and
// checks pre-flight's refusal of it. It returns the case and the
// stopped-writer copy of the v15 store taken before the raise.
func crossingV16Refused(t *testing.T, tc crossingV16Row) (*crossingCase, map[string][]byte) {
	t.Helper()
	c := newCrossingCase(t)
	h := c.h
	kept := rollbackStoreCopy(t, c.db)
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
	return c, kept
}

// newGenuineOldHome builds BuildOld's old layout with v0.11.1's genuine pair
// at bin/ and admin/, so its administrator predates the cutover
// (ErrUnknownVerb) and its list is v0.11.1's. Its store is v5, so a run
// with the move due crosses.
func newGenuineOldHome(t *testing.T, opts ...moveOldOpt) *moveHome {
	t.Helper()
	r := genuineRelease(t, "v0.11.1")
	h := newMoveHome(t)
	h.BuildOld(opts...)
	h.Copy(r.Pair.Main, filepath.Join(h.Old, "bin", "agent-director"), 0o755)
	h.Copy(r.Pair.Admin, filepath.Join(h.Old, "admin", "agent-director-admin"), 0o755)
	return h
}

// assertOldListWorks fails unless the old layout's agent-director (v0.11.1's)
// lists.
func assertOldListWorks(t *testing.T, h *moveHome) {
	t.Helper()
	if got := h.Run(filepath.Join(h.Old, "bin", "agent-director"), "list"); got.Code != 0 {
		t.Errorf("v0.11.1's list after the refusal: exit %d: %s", got.Code, got.Stderr)
	}
}

// withLinkDir makes ~/links and has h's installs write the managed PATH link
// there; it returns the directory.
func withLinkDir(h *moveHome) string {
	h.t.Helper()
	dir := filepath.Join(h.Home, "links")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	linkDirAt(h, dir)
	return dir
}

// linkDirAt has h's installs write the managed PATH link in dir
// (--symlink-dir dir in place of --no-symlink).
func linkDirAt(h *moveHome, dir string) {
	var flags []string
	for _, f := range h.Flags {
		if f != "--no-symlink" {
			flags = append(flags, f)
		}
	}
	h.Flags = setFlag(flags, "--symlink-dir", dir)
}

// restoreUnavailableAction is restore_copies_unavailable's fixed action.
const restoreUnavailableAction = "make room or write access beside the installed binaries, the store and the managed PATH link; if the sentinel beside the store is not a regular file, or an installed binary or the managed PATH entry is neither a link nor a regular file, remove that entry; then re-run this release's installer"

// copyPaths are the items a restore copies in an in-place home.
type copyPaths struct{ bin, admin, sentinel, link, linkDir string }

// newCopiesCrossing is a crossingCase whose v0.17.3 install has a managed
// PATH link (~/links/agent-director) and a sentinel an earlier run left
// beside the store, so every item a restore copies is there.
func newCopiesCrossing(t *testing.T) (*crossingCase, copyPaths) {
	t.Helper()
	var p copyPaths
	c := newCrossingCase(t, func(h *moveHome) { p.linkDir = withLinkDir(h) })
	h := c.h
	p.bin, p.admin = filepath.Join(h.New, "bin", "agent-director"), filepath.Join(h.New, "admin", "agent-director-admin")
	p.sentinel, p.link = filepath.Join(h.New, "migrate-authorized"), filepath.Join(p.linkDir, "agent-director")
	if target, err := os.Readlink(p.link); err != nil || target != p.bin {
		t.Fatalf("v0.17.3's managed link %s names %q (%v); want %s", p.link, target, err, p.bin)
	}
	h.Write(p.sentinel, "{\"from\": 14, \"to\": 15}\n", 0o600)
	return c, p
}

// unwritable takes write access from dir; the returned func gives it back.
func unwritable(t *testing.T, dir string) func() {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	mode := fi.Mode().Perm()
	if err := os.Chmod(dir, mode&^0o222); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, mode) })
	return func() {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
}

// standInBSDStat makes stat answer as BSD's: -c fails, and -f with the forms
// install.sh uses (%d:%i, %Lp, %m, %u) answers as GNU's -c does. Each call is
// logged to the returned file.
func standInBSDStat(h *moveHome) string {
	log := filepath.Join(h.Root, "stat.log")
	h.WriteTool("stat", `echo "stat $*" >>`+moveQuote(log)+`
args=() f=""
while (($#)); do
    case "$1" in
        -c) exit 1 ;;
        -f) f="$2"; shift 2; continue ;;
    esac
    args+=("$1"); shift
done
case "$f" in
    "") exec `+moveQuote(h.RealTool("stat"))+` "${args[@]}" ;;
    %Lp) f=%a ;;
    %m) f=%Y ;;
esac
exec `+moveQuote(h.RealTool("stat"))+` -c "$f" "${args[@]}"`)
	return log
}

// TestAdviceFollow_J36_BackupInsideInstallDirectory: with the old-layout move
// due from v0.11.1, a journal backup directory inside AD's old or new
// directory refuses in pre-flight with nothing changed, judged physically
// (SR-2.14) and on the Darwin branch by file identity, a case variant
// included (SR-2.15 "Limits"); the advice, followed literally, gives a
// passing upgrade that moves and crosses. On Linux the case variants pass the
// check, and an in-place upgrade never runs it. J36: "choose a
// --journal-backup directory outside AD's old and new directories
// (~/.agent-director and ~/.config/agent-director), then re-run this
// release's installer".
//
// --check-only refuses a backup whose parent is missing or cannot be looked
// up (backup_unreadable) before this check runs, so a missing ~/.config is
// reached through a dangling link, a case variant's parent exists, and the
// place that cannot be judged is a Darwin identity that cannot be read.
//
// When it is AD's directory whose place cannot be judged (~/.config a file,
// or on Darwin an identity above the old directory that cannot be read), a
// backup already outside refuses with the repair advice instead: "repair the
// path of AD's directory that the message names (...), then choose a
// --journal-backup directory outside ...". Followed literally, the named
// repair alone, with the same backup, gives a passing upgrade.
func TestAdviceFollow_J36_BackupInsideInstallDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds v0.11.1 and v0.17.3")
	}
	const action = "choose a --journal-backup directory outside AD's old and new directories (~/.agent-director and ~/.config/agent-director), then re-run this release's installer"
	const repairAction = "repair the path of AD's directory that the message names (rename aside any existing part of it that is not a directory, make each existing part one the user running the install can enter, break any link loop in it, and make its file identity readable), then " + action
	mkdir := func(t *testing.T, dir string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		// in is the directory the backup is inside, "old" or "new"; "" when
		// the check passes it.
		in       string
		unjudged bool // its place cannot be judged
		darwin   bool
		bsdStat  bool
		backup   func(t *testing.T, h *moveHome) string
		// repair, for a backup already outside whose check fails on the
		// install directory's side (status 2), is the repair stderr names,
		// done in place of choosing another backup; cause is the line saying
		// why.
		repair func(t *testing.T, h *moveHome)
		cause  func(h *moveHome) string
	}{
		{name: "lexically inside the old directory", in: "old", backup: func(t *testing.T, h *moveHome) string {
			return filepath.Join(h.Old, "backup")
		}},
		{name: "through a link into the old directory", in: "old", backup: func(t *testing.T, h *moveHome) string {
			h.Link(h.Old, filepath.Join(h.Root, "via"))
			return filepath.Join(h.Root, "via", "backup")
		}},
		{name: "by its text through an inner link leading out", in: "old", backup: func(t *testing.T, h *moveHome) string {
			mkdir(t, filepath.Join(h.Root, "elsewhere"))
			h.Link(filepath.Join(h.Root, "elsewhere"), filepath.Join(h.Old, "out"))
			return filepath.Join(h.Old, "out", "backup")
		}},
		{name: "under the new directory with no ~/.config yet", in: "new", backup: func(t *testing.T, h *moveHome) string {
			h.Link(h.New, filepath.Join(h.Root, "to-new"))
			return filepath.Join(h.Root, "to-new", "backup")
		}},
		{name: "darwin identity that cannot be read", in: "old", unjudged: true, darwin: true, backup: func(t *testing.T, h *moveHome) string {
			odd := filepath.Join(h.Root, "odd")
			mkdir(t, odd)
			h.WriteTool("stat", `for a; do :; done
if [[ "$a" == `+moveQuote(odd)+` ]]; then exit 1; fi
exec `+moveQuote(h.RealTool("stat"))+` "$@"`)
			return filepath.Join(odd, "backup")
		}},
		{name: "install side: ~/.config is a file", in: "new", unjudged: true,
			backup: func(t *testing.T, h *moveHome) string {
				config := filepath.Dir(h.New)
				if err := os.Remove(config); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				h.Write(config, "not a directory\n", 0o644)
				return filepath.Join(h.Root, "backup-outside")
			},
			cause: func(h *moveHome) string {
				return "  " + h.New + " cannot be placed: an existing part of it is not a directory or cannot be entered, or its links loop"
			},
			repair: func(t *testing.T, h *moveHome) {
				config := filepath.Dir(h.New)
				if err := os.Rename(config, config+".aside"); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "install side: darwin identity of the old directory's parent cannot be read", in: "old", unjudged: true, darwin: true,
			backup: func(t *testing.T, h *moveHome) string {
				h.WriteTool("stat", `for a; do :; done
if [[ "$a" == `+moveQuote(h.Home)+` && " $* " == *" %d:%i "* ]]; then exit 1; fi
exec `+moveQuote(h.RealTool("stat"))+` "$@"`)
				return filepath.Join(h.Root, "backup-outside")
			},
			cause: func(h *moveHome) string {
				return "  the file identity of " + h.Old + ", or of a directory above it, cannot be read"
			},
			repair: func(t *testing.T, h *moveHome) {
				if err := os.Remove(filepath.Join(h.Tools, "stat")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "darwin case variant of the old directory with BSD stat", in: "old", darwin: true, bsdStat: true, backup: func(t *testing.T, h *moveHome) string {
			mkdir(t, filepath.Join(h.Home, ".Agent-Director"))
			return filepath.Join(h.Home, ".Agent-Director", "backup")
		}},
		{name: "darwin case variant of the new directory not made yet", in: "new", darwin: true, backup: func(t *testing.T, h *moveHome) string {
			mkdir(t, filepath.Join(h.Home, ".config", "Agent-Director"))
			return filepath.Join(h.Home, ".config", "Agent-Director", "b")
		}},
		{name: "linux case variant of the old directory", backup: func(t *testing.T, h *moveHome) string {
			mkdir(t, filepath.Join(h.Home, ".Agent-Director"))
			return filepath.Join(h.Home, ".Agent-Director", "backup")
		}},
		{name: "linux case variant of the new directory", backup: func(t *testing.T, h *moveHome) string {
			mkdir(t, filepath.Join(h.Home, ".config", "Agent-Director"))
			return filepath.Join(h.Home, ".config", "Agent-Director", "b")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGenuineOldHome(t)
			if tc.darwin {
				standInDarwin(h)
			}
			var statLog string
			if tc.bsdStat {
				statLog = standInBSDStat(h)
			}
			backup := tc.backup(t, h)
			h.Flags = setFlag(h.Flags, "--journal-backup", backup)
			before := h.PhysicalSnapshot()
			res := h.Install()
			if tc.in == "" {
				// A new tree under HOME holds the backup, so the move's
				// whole-home check does not apply.
				assertMovedCrossed(t, h, nil, res, backup)
				return
			}
			dir := map[string]string{"old": h.Old, "new": h.New}[tc.in]
			want := action
			lines := []string{"install.sh: the journal backup directory " + backup + " is inside AD's " + tc.in + " directory " + dir + "; refusing to upgrade."}
			if tc.unjudged {
				lines = []string{"install.sh: cannot judge whether the journal backup directory " + backup + " is inside AD's " + tc.in + " directory " + dir + "; refusing to upgrade.",
					"  the file identity of " + filepath.Dir(backup) + ", or of a directory above it, cannot be read"}
			}
			if tc.repair != nil {
				want = repairAction
				lines = []string{lines[0], tc.cause(h),
					"  To fix: repair the path " + dir + " as the action below says; choosing another --journal-backup alone does not clear this."}
			} else if strings.Contains(res.Stderr, "  To fix: ") {
				t.Errorf("a backup-side refusal says to repair AD's directory:\n%s", res.Stderr)
			}
			lines = append(lines, "  This upgrade moves "+h.Old+" to "+h.New+", and a restore renames those directories, which would move or hide a backup inside them.")
			assertOwnRefusal(t, res, "backup_inside_install_directory", want, lines...)
			assertRefusedUnchanged(t, h, before)
			for _, p := range []string{h.New, backup} {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("the refusal made %s", p)
				}
			}
			assertOldListWorks(t, h)
			if statLog != "" {
				if b, _ := os.ReadFile(statLog); !strings.Contains(string(b), "stat -L -f %d:%i ") {
					t.Errorf("the identity rule did not read BSD stat's -f '%%d:%%i':\n%s", b)
				}
			}

			outside := filepath.Join(h.Root, "backup-outside")
			if tc.repair != nil {
				// The backup is outside already; only the named repair is done.
				tc.repair(t, h)
			} else {
				h.Flags = setFlag(h.Flags, "--journal-backup", outside)
			}
			moved := h.Snapshot()
			assertMovedCrossed(t, h, moved, h.Install(), outside)
			if tc.darwin {
				assertDarwinOnly(t, h)
			}
		})
	}
	t.Run("in place never checks", func(t *testing.T) {
		c := newCrossingCase(t)
		backup := filepath.Join(c.h.New, "journal-backup")
		c.assertCrossed(t, c.h.Install(setFlag(c.valid(), "--journal-backup", backup)...), backup)
	})
}

// TestAdviceFollow_J37_RestoreCopiesUnavailable: from a genuine v0.17.3
// install with a managed PATH link and a leftover sentinel, a copy the
// restore needs that cannot be made refuses in pre-flight with nothing
// changed and no copy left, the earlier items' copies removed; following the
// advice for the item stderr names gives a passing crossing upgrade that
// writes the managed link again. J37: "make room or write access beside the
// installed binaries, the store and the managed PATH link; if the sentinel
// beside the store is not a regular file, or an installed binary or the
// managed PATH entry is neither a link nor a regular file, remove that entry;
// then re-run this release's installer".
func TestAdviceFollow_J37_RestoreCopiesUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
	}
	const action = restoreUnavailableAction
	remove := func(t *testing.T, p string) func() {
		return func() {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name string
		item func(p copyPaths) string
		// cause is the cause's exact text, or "" when it is the tool's own.
		cause func(p copyPaths) string
		// block makes the item's copy impossible and returns the advice's
		// step for it.
		block func(t *testing.T, h *moveHome, p copyPaths) func()
	}{
		{"binary directory unwritable", func(p copyPaths) string { return "agent-director, " + p.bin }, nil,
			func(t *testing.T, h *moveHome, p copyPaths) func() { return unwritable(t, filepath.Dir(p.bin)) }},
		{"administrator directory unwritable", func(p copyPaths) string { return "agent-director-admin, " + p.admin }, nil,
			func(t *testing.T, h *moveHome, p copyPaths) func() { return unwritable(t, filepath.Dir(p.admin)) }},
		{"store directory unwritable", func(p copyPaths) string { return "the migration sentinel, " + p.sentinel }, nil,
			func(t *testing.T, h *moveHome, p copyPaths) func() { return unwritable(t, filepath.Dir(p.sentinel)) }},
		{"link directory unwritable", func(p copyPaths) string { return "the managed PATH link, " + p.link }, nil,
			func(t *testing.T, h *moveHome, p copyPaths) func() { return unwritable(t, p.linkDir) }},
		{"sentinel is a link", func(p copyPaths) string { return "the migration sentinel, " + p.sentinel },
			func(p copyPaths) string { return p.sentinel + " is not a regular file" },
			func(t *testing.T, h *moveHome, p copyPaths) func() {
				target := filepath.Join(h.Root, "sentinel-target")
				h.Write(target, "{\"from\": 14, \"to\": 15}\n", 0o600)
				remove(t, p.sentinel)()
				h.Link(target, p.sentinel)
				return remove(t, p.sentinel)
			}},
		{"managed PATH entry is a directory", func(p copyPaths) string { return "the managed PATH link, " + p.link },
			func(p copyPaths) string { return p.link + " is neither a link nor a regular file" },
			func(t *testing.T, h *moveHome, p copyPaths) func() {
				remove(t, p.link)()
				if err := os.Mkdir(p.link, 0o755); err != nil {
					t.Fatal(err)
				}
				return remove(t, p.link)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := newCopiesCrossing(t)
			h := c.h
			follow := tc.block(t, h, p)
			flags := c.valid()
			before := h.PhysicalSnapshot()
			res := h.Install(flags...)
			lines := []string{"install.sh: cannot make the copies a restore needs; refusing to upgrade.", "  item : " + tc.item(p), "  cause:"}
			if tc.cause != nil {
				lines = append(lines, "    "+tc.cause(p))
			}
			assertOwnRefusal(t, res, "restore_copies_unavailable", action, lines...)
			assertRefusedUnchanged(t, h, before, ".config/agent-director", ".config/agent-director/bin", ".config/agent-director/admin", "links")

			// v0.17.3's list may need to write beside its store, so it runs
			// once the advice has given write access back.
			follow()
			if got := h.Run(c.prev, "list"); got.Code != 0 {
				t.Errorf("v0.17.3's list after the refusal: exit %d: %s", got.Code, got.Stderr)
			}
			c.assertCrossed(t, h.Install(flags...), journalBackup(flags))
			if target, err := os.Readlink(p.link); err != nil || target != p.bin {
				t.Errorf("the managed link %s names %q (%v); want %s", p.link, target, err, p.bin)
			}
			if got := restoreCopyNames(t, h); len(got) != 0 {
				t.Errorf("copies left: %v", got)
			}
		})
	}
}

// probeStandIn puts a script at admin that logs its arguments to the
// returned file, emptied first, then runs body.
func probeStandIn(h *moveHome, admin, body string) string {
	h.t.Helper()
	log := filepath.Join(h.Root, "probe.log")
	for _, p := range []string{admin, log} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			h.t.Fatal(err)
		}
	}
	h.Write(admin, "#!/bin/bash\nprintf '%s\\n' \"$*\" >>"+moveQuote(log)+"\n"+body+"\n", 0o755)
	return log
}

// assertNotProbed fails unless res, a passing run, did not probe the previous
// administrator: no "previous:" line, and log holds no probe.
func assertNotProbed(t *testing.T, res moveResult, log string) {
	t.Helper()
	b, _ := os.ReadFile(log)
	if strings.Contains(res.Stdout, "  previous: ") || strings.Contains(string(b), "migrate-journal --help") {
		t.Errorf("a run that does not cross probed the previous administrator (log %q):\n%s", b, res.Stdout)
	}
}

// TestAdviceFollow_J38_PreviousReleaseUnknown: a crossing upgrade whose
// previous administrator gives the predates probe no clear answer refuses in
// pre-flight with nothing changed, after running it once as exactly
// migrate-journal --help; the advice followed down either branch (made
// runnable, or removed so that it is absent) gives a passing crossing
// upgrade. A run that does not cross never probes. J38: "make the previous
// agent-director-admin runnable, or remove it if it is not that release's,
// then re-run this release's installer".
func TestAdviceFollow_J38_PreviousReleaseUnknown(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3 and v0.11.1")
	}
	const action = "make the previous agent-director-admin runnable, or remove it if it is not that release's, then re-run this release's installer"
	script := func(body string) func(h *moveHome, admin string) string {
		return func(h *moveHome, admin string) string { return probeStandIn(h, admin, body) }
	}
	for _, tc := range []struct {
		name   string
		status int
		// unclear makes the administrator at admin give no clear answer and
		// returns the file a script stand-in logs its arguments to, or "".
		unclear  func(h *moveHome, admin string) string
		runnable bool // the follow makes it runnable, rather than removing it
		moveDue  bool
		// nonCrossing then shows that runs that do not cross never probe.
		nonCrossing bool
	}{
		{name: "not executable", status: 126, runnable: true, unclear: func(h *moveHome, admin string) string {
			if err := os.Chmod(admin, 0o644); err != nil {
				h.t.Fatal(err)
			}
			return ""
		}},
		{name: "dangling link", status: 127, unclear: func(h *moveHome, admin string) string {
			if err := os.Remove(admin); err != nil {
				h.t.Fatal(err)
			}
			h.Link(filepath.Join(h.Root, "gone"), admin)
			return ""
		}},
		{name: "exit 127", status: 127, unclear: script("exit 127")},
		{name: "exit 3", status: 3, unclear: script("exit 3"), nonCrossing: true},
		{name: "exit 1 with no envelope", status: 1, unclear: script("echo 'not an envelope' >&2; exit 1")},
		{name: "exit 1 with another err_name", status: 1, unclear: script(`echo '{"err_name":"ErrInvalidFlags","err_description":"stand-in"}' >&2; exit 1`)},
		{name: "killed by a signal", status: 137, unclear: script("kill -KILL $$")},
		{name: "move due", status: 3, unclear: script("exit 3"), moveDue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c *crossingCase
			var h *moveHome
			var admin string
			var flags []string
			if tc.moveDue {
				h = newGenuineOldHome(t)
				admin = filepath.Join(h.Old, "admin", "agent-director-admin")
			} else {
				c = newCrossingCase(t)
				h = c.h
				admin = filepath.Join(h.New, "admin", "agent-director-admin")
				flags = c.valid()
			}
			log := tc.unclear(h, admin)
			before := h.PhysicalSnapshot()
			res := h.Install(flags...)
			assertOwnRefusal(t, res, "previous_release_unknown", action,
				"install.sh: cannot tell whether the previous release predates the native-input cutover; refusing to upgrade.",
				"  previous agent-director-admin: "+admin,
				fmt.Sprintf("  migrate-journal --help exited with status %d, without exit 0 or exit 1 with ErrUnknownVerb. It said:", tc.status))
			assertRefusedUnchanged(t, h, before)
			if log != "" {
				if b, err := os.ReadFile(log); err != nil || string(b) != "migrate-journal --help\n" {
					t.Errorf("the probe ran the administrator with %q (%v); want once, as exactly migrate-journal --help", b, err)
				}
			}
			if tc.moveDue {
				if _, err := os.Lstat(h.New); err == nil {
					t.Errorf("the refusal made %s", h.New)
				}
				assertOldListWorks(t, h)
			} else if got := h.Run(c.prev, "list"); got.Code != 0 {
				t.Errorf("v0.17.3's list after the refusal: exit %d: %s", got.Code, got.Stderr)
			}

			why := "absent"
			if tc.runnable {
				if err := os.Chmod(admin, 0o755); err != nil {
					t.Fatal(err)
				}
				why = "migrate-journal --help: ErrUnknownVerb"
			} else if err := os.Remove(admin); err != nil {
				t.Fatal(err)
			}
			if tc.moveDue {
				moved := h.Snapshot()
				res = h.Install()
				assertMovedCrossed(t, h, moved, res, journalBackup(h.Flags))
			} else {
				res = h.Install(flags...)
				c.assertCrossed(t, res, journalBackup(flags))
			}
			if want := "  previous: " + admin + " predates the native-input cutover (" + why + ")\n"; !strings.Contains(res.Stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, res.Stdout)
			}
			if !tc.nonCrossing {
				return
			}
			// The store is current now, so with an unclear administrator
			// again the run does not cross and passes unprobed; so does a run
			// with no store.
			log = probeStandIn(h, admin, "exit 3")
			assertNotProbed(t, moveMustInstall(t, h), log)
			fresh := newMoveHome(t)
			log = probeStandIn(fresh, filepath.Join(fresh.New, "admin", "agent-director-admin"), "exit 3")
			assertNotProbed(t, moveMustInstall(t, fresh), log)
		})
	}
}
