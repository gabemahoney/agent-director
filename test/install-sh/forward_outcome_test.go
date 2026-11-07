package installsh_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// J40: "Re-run this release's installer with a new backup directory to finish
// any pending schema step", then the printed verification retry until exit 0
// with authority sqlite. J39 follows supported retries; moved source files get Q4's note.
// J44 and J45 run the unestablished, v0.16.0 residual and moved-script rows.
func TestAdviceFollow_J40_ForwardOutcomes(t *testing.T) {
	for _, tc := range []forwardOutcomeCase{
		{name: "v0.17.3 committed", mode: "child:verify-fails-3", cutover: "committed", reason: "committed_verification_incomplete"},
		{name: "v0.17.3 unknown commit", mode: "child:cutover-unconfirmed-committed", cutover: "unknown", reason: "migration_incomplete_or_commit_unknown"},
		{name: "v0.17.3 administrator lock", lock: true, piped: true, cutover: "unknown", reason: "migration_lock_held"},
		{name: "J39 v0.11.1 moved cwd", residual: true, moved: true, rebase: true, mode: "child:verify-fails-2", cutover: "not_committed", reason: "old_or_unverified_mutators"},
		{name: "J39 v0.11.1 outside relative paths and logical cwd", residual: true, moved: true, relativePaths: true, mode: "child:verify-fails-2", cutover: "not_committed", reason: "old_or_unverified_mutators"},
		{name: "J39 v0.11.1 moved sources piped note", residual: true, moved: true, rebase: true, movedSources: true, piped: true, mode: "child:verify-fails-2", cutover: "not_committed", reason: "old_or_unverified_mutators"},
		{name: "J39 current release over v15", rerun: true, piped: true, cutover: "not_committed", reason: "old_or_unverified_mutators"},
		{name: "v0.11.1 link reached store changes", moved: true, linked: true, cutover: "not_committed", reason: "old_or_unverified_mutators"},
		{name: "v0.17.3 unestablished closed stderr", unestablished: true, closed: true, cutover: "unknown"},
		{name: "v0.17.3 unreadable cutover", mode: "canned", cutover: "unknown", reason: "ErrStoreOpen"},
		{name: "v0.17.3 Darwin lock", lock: true, darwin: true, piped: true, cutover: "unknown", reason: "migration_lock_held"},
	} {
		t.Run(tc.name, func(t *testing.T) { forwardOutcome(t, tc, nil) })
	}
}

// forwardOutcomeCase is one J40 row.
type forwardOutcomeCase struct {
	name, mode, cutover, reason                                                        string
	residual, moved, linked, lock, rerun, unestablished, closed, darwin, piped, rebase bool
	relativePaths, movedScript, movedSources                                           bool
}

// forwardOutcome runs one J40 row. wayBack, when set, follows another way on
// from the same outcome: it runs once, after the row's outcome checks and
// before its forward follow, and must leave the home as it found it.
func forwardOutcome(t *testing.T, tc forwardOutcomeCase, wayBack func(f *cutoverFixture, res moveResult, env cutoverEnvelope, db string)) {
	var h *moveHome
	var db string
	if tc.moved {
		h = newGenuineOldHome(t,
			withMoveStore(func(h *moveHome) string { return filepath.Join(h.Home, "elsewhere", "state.db") }),
			withMoveConfig(func(h *moveHome) string {
				path := filepath.Join(h.Home, "elsewhere", "state.db")
				if tc.linked {
					path = filepath.Join(h.Old, "out", "state.db")
				}
				return moveOldConfig + "db_path = " + strconv.Quote(path) + "\n"
			}))
		db = filepath.Join(h.Home, "elsewhere", "state.db")
		if tc.linked {
			h.Link(filepath.Dir(db), filepath.Join(h.Old, "out"))
		}
		linksInHome(h)
		h.Link(filepath.Join(h.Old, "bin", "agent-director"), filepath.Join(h.Tools, "agent-director"))
		h.Write(filepath.Join(h.Old, "templates", "reference.toml"), "name = \"reference\"\ncwd = "+strconv.Quote(filepath.Join(h.Old, "work"))+"\n", 0o600)
		h.Env["PATH"] = h.Tools + ":" + filepath.Join(h.Old, "bin") + ":" + h.S.Toolbox
	} else if tc.residual {
		h = newMoveHome(t)
		h.InstallRelease(genuineRelease(t, "v0.16.0"))
		db = filepath.Join(h.New, "state.db")
		if got := moveUserVersion(t, h, db); got != "14" {
			t.Fatalf("v0.16.0 store v%s; want 14", got)
		}
		h.authorizeJournalMigration()
	} else {
		c, _ := newCopiesCrossing(t)
		h, db = c.h, c.db
		h.Flags = append(h.Flags, c.valid()...)
	}
	f := newCutoverFixture(t, h)
	if tc.mode != "" {
		f.Choose(tc.mode)
	}
	retryLink := filepath.Join(h.Home, "links", "agent-director")
	if tc.rebase {
		h.Cwd = filepath.Join(h.Old, "retry-dir")
		h.Write(filepath.Join(h.Cwd, "cwd-marker"), "keep this working directory\n", 0o600)
	}
	if tc.movedScript {
		s := *h.S
		h.Copy(s.Install, filepath.Join(h.Cwd, "install.sh"), 0o755)
		s.Install = "./install.sh"
		h.S = &s
	}
	if tc.movedSources {
		h.Copy(h.Pair.Main, filepath.Join(h.Cwd, "candidate-main"), 0o755)
		h.Copy(h.Pair.Admin, filepath.Join(h.Cwd, "candidate-admin"), 0o755)
		h.Pair = movePair{"./candidate-main", "./candidate-admin"}
	}
	if tc.relativePaths {
		// Q4 keeps the logical cwd and raw ../ spellings. All these
		// paths remain outside the moved install and can be followed.
		dir := filepath.Join(h.Home, "release-root")
		h.Write(filepath.Join(dir, "work", "cwd-marker"), "logical cwd\n", 0o600)
		h.Cwd = filepath.Join(h.Home, "caller")
		h.Link(filepath.Join(dir, "work"), h.Cwd)
		h.Env["PWD"] = h.Cwd
		s := *h.S
		h.Copy(s.Install, filepath.Join(dir, "install.sh"), 0o755)
		s.Install = "../install.sh"
		h.S = &s
		h.Copy(h.Pair.Main, filepath.Join(dir, "candidate-main"), 0o755)
		h.Copy(h.Pair.Admin, filepath.Join(dir, "candidate-admin"), 0o755)
		h.Pair = movePair{"../candidate-main", "../candidate-admin"}
		retryLink = filepath.Join(dir, "links", "agent-director")
		h.Link(filepath.Join(h.Old, "bin", "agent-director"), retryLink)
		h.Flags = setFlag(h.Flags, "--symlink-dir", "../links")
	}
	var release func()
	var lockBefore os.FileInfo
	if tc.lock || tc.rerun {
		lock := db + ".input-migration.lock"
		// Only this shell owns the flock, and read is a shell builtin,
		// so Kill cannot leave a lock-holding child behind.
		holder := h.startProc(exec.Command(h.S.Bash, "-c", `exec 9>"$1"; flock -x 9; echo ready; read -r done`, "lock", lock), "", "ready")
		release = holder.Kill
		var err error
		lockBefore, err = os.Stat(lock)
		if err != nil {
			t.Fatal(err)
		}
		if tc.rerun {
			f.Check(f.Install(false, false), "ErrJournalMigrationIncomplete", "unknown", false)
			holder.Kill()
			mutator := h.HoldSQLite(db, "PRAGMA user_version;")
			release = mutator.Kill
			h.Flags = setFlag(h.Flags, "--journal-backup", filepath.Join(h.Root, "second-backup"))
		}
	}
	if tc.unestablished || tc.mode == "canned" || tc.linked {
		body := "echo 'panic: injected administrator failure' >&2\nexit 2\n"
		if tc.mode == "canned" {
			body = "echo 'administrator diagnostic' >&2\necho '{\"err_name\":\"ErrStoreOpen\",\"err_description\":\"injected\"}' >&2\nexit 1\n"
		}
		if tc.linked {
			body = "echo '{\"err_name\":\"ErrJournalMigrationRefused\",\"err_description\":\"injected\",\"details\":{\"refusal\":{\"reason\":\"old_or_unverified_mutators\",\"action\":\"stop them\",\"processes\":[]},\"cutover\":\"not_committed\",\"steps_committed\":false}}' >&2\nexit 1\n"
		}
		h.Write(f.Control+".response", body, 0o600)
		f.Choose("canned")
	}
	if tc.darwin {
		standInDarwin(h)
	}
	beforeCopies := map[string]string{}
	if tc.unestablished {
		for _, p := range []string{filepath.Join(h.New, "bin", "agent-director"), filepath.Join(h.New, "admin", "agent-director-admin"), filepath.Join(h.New, "migrate-authorized"), filepath.Join(h.Home, "links", "agent-director")} {
			beforeCopies[p] = restoreDescribe(t, p)
		}
	}
	var res moveResult
	oldVersion := moveUserVersion(t, h, db)
	if tc.linked {
		f.Block("canned")
		running := h.StartInstall()
		f.Await()
		v, err := strconv.Atoi(oldVersion)
		if err != nil {
			t.Fatal(err)
		}
		h.Sqlite(db, fmt.Sprintf("PRAGMA user_version=%d;", v+1))
		f.Continue()
		res = running.Wait(t)
	} else {
		res = f.Install(tc.piped, tc.closed)
	}
	if release != nil {
		release()
	}
	if tc.lock {
		after, err := os.Stat(db + ".input-migration.lock")
		if err != nil || !os.SameFile(lockBefore, after) {
			t.Errorf("held lock replaced: %v", err)
		}
	}
	for _, pair := range [][2]string{{h.S.Current.Main, filepath.Join(h.New, "bin", "agent-director")}, {h.S.ScheduleProbe.Main, filepath.Join(h.New, "admin", "agent-director-admin")}} {
		if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
			t.Errorf("candidate not retained at %s", pair[1])
		}
	}
	if tc.closed {
		if res.Code != 5 || res.Stderr != "" {
			t.Errorf("closed stderr: exit %d, %q", res.Code, res.Stderr)
		}
		copies := restoreCopyNames(t, h)
		if len(copies) != len(beforeCopies) {
			t.Fatalf("kept copies: %v; want %d", copies, len(beforeCopies))
		}
		for belongs, desc := range beforeCopies {
			found := false
			for _, rel := range copies {
				p := filepath.Join(h.Home, rel)
				if strings.HasPrefix(p, belongs+".restore-") {
					if info, _ := os.Lstat(p); info.IsDir() {
						p = filepath.Join(p, filepath.Base(belongs))
					}
					if restoreDescribe(t, p) != desc {
						t.Errorf("kept copy differs: %s", p)
					}
					found = true
				}
			}
			if !found {
				t.Errorf("no kept copy for %s", belongs)
			}
		}
		return
	}
	env := f.Check(res, "ErrJournalMigrationIncomplete", tc.cutover, tc.unestablished)
	if env.Details.Refusal["reason"] != tc.reason {
		t.Errorf("reason %v; want %s", env.Details.Refusal["reason"], tc.reason)
	}
	if tc.unestablished {
		if env.Details.AttemptStore != db || len(env.Details.Remove) != 0 || len(env.Details.Copies) != len(beforeCopies) {
			t.Errorf("kept outcome: %+v", env.Details)
		}
		for _, copy := range env.Details.Copies {
			if want, ok := beforeCopies[copy.Belongs]; !ok || restoreDescribe(t, copy.Copy) != want {
				t.Errorf("kept copy differs: %+v", copy)
			}
		}
		outcomeLines(t, res.Stderr, "install.sh: the installer could not establish how the attempt ended (migrate-journal exited 2 with no error envelope); this release stays installed and the copies are kept.")
	}
	if tc.cutover == "not_committed" {
		outcomeLines(t, res.Stderr, "install.sh: the cutover did not commit; this release stays installed.", "  To complete forward, fix the reported cause, then re-run this release's installer with a new backup directory:")
		if tc.residual {
			if v := moveUserVersion(t, h, db); v != "15" {
				t.Errorf("residual store v%s; want 15", v)
			}
			outcomeLines(t, res.Stderr,
				"  This attempt committed earlier schema steps before the cutover, so the store has moved past the previous release.",
				"  The installer cannot put the previous release back: the store has moved, or may have moved, past it.",
				"  To go back instead, restore the store from the journal backup directory "+journalBackup(h.Flags)+" (filled before any schema step), and install the previous release with its own installer, as docs/operator-actions.md \"Going back to the previous release\" describes.")
		}
		if tc.rerun {
			outcomeLines(t, res.Stderr, "  The administrator installed before the run ("+filepath.Join(h.New, "admin", "agent-director-admin")+") does not predate the cutover, so there is no earlier release to put back.")
			if strings.Contains(res.Stdout, "  restore : copied") {
				t.Error("current prior release made restore copies")
			}
		}
		if tc.linked {
			v, _ := strconv.Atoi(oldVersion)
			outcomeLines(t, res.Stderr, fmt.Sprintf("  The store %s was at v%s in pre-flight and reads v%d after the attempt.", db, oldVersion, v+1))
		}
	} else {
		if !tc.unestablished {
			outcomeLines(t, res.Stderr, "install.sh: the cutover attempt did not finish successfully; this release stays installed.")
		}
		commitLine := "  Whether the cutover committed is unknown."
		if tc.cutover == "committed" {
			commitLine = "  The cutover committed: the store's input is now in SQLite and the previous release cannot open it."
		}
		outcomeLines(t, res.Stderr, commitLine,
			"  Never reset the schema or restore replayable input. Keep the maintenance boundary through verification.",
			"  Fix the reported cause; for migration_lock_held, let the other administrator run end.",
			"  Re-run this release's installer with a new backup directory to finish any pending schema step:",
			"  Then run the verification retry until it exits 0 and its result shows \"authority\": \"sqlite\", choosing a new verification backup directory for each attempt:")
	}
	if tc.moved {
		for _, prefix := range []string{"  kept    : ", "  old link: ", "  old PATH: ", "  references: "} {
			if n := strings.Count(res.Stdout, prefix); n != 1 {
				t.Errorf("%q count %d; want 1:\n%s", prefix, n, res.Stdout)
			}
		}
		if _, err := os.Lstat(h.Old); !os.IsNotExist(err) {
			t.Errorf("old directory still exists: %v", err)
		}
	}
	movedNote := "  Installer or source files named under " + h.Old + " now sit under " + h.New + "/...; re-run from wherever you keep the installer."
	if tc.movedScript || tc.movedSources {
		outcomeLines(t, res.Stderr, movedNote)
		if strings.Count(res.Stderr, movedNote+"\n") != 1 {
			t.Error("moved-file note must appear once")
		}
		// Q4 deliberately leaves locating these files to the operator.
		if wayBack != nil {
			wayBack(f, res, env, db)
		}
		return
	}
	if strings.Contains(res.Stderr, "Installer or source files named under ") {
		t.Errorf("unexpected moved-file note:\n%s", res.Stderr)
	}
	if tc.rebase || tc.relativePaths {
		cwd, script, main, admin, links := filepath.Join(h.New, "retry-dir"), h.S.Install, h.Pair.Main, h.Pair.Admin, filepath.Dir(retryLink)
		if tc.relativePaths {
			cwd = h.Cwd
			script, main, admin, links = cwd+"/../install.sh", cwd+"/../candidate-main", cwd+"/../candidate-admin", cwd+"/../links"
		}
		for _, p := range []string{cwd, script, main, admin, links} {
			if !schedBareWord.MatchString(p) {
				t.Fatalf("expected retry path needs shell quoting: %q", p)
			}
		}
		for _, part := range []string{"    cd " + cwd + " && bash " + script + " ", " --binary " + main + " ", " --admin-binary " + admin + " ", " --symlink-dir " + links + "\n"} {
			if !strings.Contains(res.Stderr, part) {
				t.Errorf("retry command missing exact path spelling %q:\n%s", part, res.Stderr)
			}
		}
	}
	if tc.linked {
		return
	} // changing user_version alone is not a valid schema step
	if wayBack != nil {
		wayBack(f, res, env, db)
	}
	f.Clear()
	h.Write(f.Calls, "", 0o600)
	backup := filepath.Join(h.Root, "follow-backup")
	follow := f.Follow(res, tc.piped, backup)
	if tc.rebase {
		h.Cwd = filepath.Join(h.New, "retry-dir")
	}
	if tc.rebase || tc.relativePaths {
		// The printed command has already run unchanged. Subsequent
		// schema-reference checks use stable build paths, not the original
		// relative arguments whose meaning depended on the old cwd.
		h.Pair = movePair{h.S.Current.Main, h.S.ScheduleProbe.Main}
	}
	if tc.cutover == "committed" || tc.mode == "child:cutover-unconfirmed-committed" {
		if follow.Code != 0 {
			t.Fatalf("non-crossing follow: exit %d: %s", follow.Code, follow.Stderr)
		}
		calls, _ := os.ReadFile(f.Calls)
		if strings.Contains(follow.Stdout, "  cutover :") || strings.Contains(string(calls), "--check-only") {
			t.Errorf("recorded authority ran crossing preflight:\n%s\n%s", follow.Stdout, calls)
		}
	} else {
		(&crossingCase{h: h, db: db}).assertCrossed(t, follow, backup)
	}
	if tc.rebase || tc.relativePaths {
		canonical := filepath.Join(h.New, "bin", "agent-director")
		for _, pair := range [][2]string{{h.S.Current.Main, canonical}, {h.S.ScheduleProbe.Main, filepath.Join(h.New, "admin", "agent-director-admin")}} {
			if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
				t.Errorf("retry installed the wrong source at %s", pair[1])
			}
		}
		if got, err := os.Readlink(retryLink); err != nil || got != canonical {
			t.Errorf("retry managed link: %q, %v; want %s", got, err, canonical)
		}
		// The link already existed after the refusal: require this retry
		// to manage it, rather than silently skip a misresolved directory.
		printedLink := retryLink
		if tc.relativePaths {
			printedLink = h.Cwd + "/../links/agent-director"
		}
		outcomeLines(t, follow.Stdout, "  symlink : "+printedLink+" → "+canonical)
	}
	if tc.cutover != "not_committed" {
		verified := f.runAdvice(res, "<new-verification-backup-directory>", filepath.Join(h.Root, "verification-backup"))
		var answer struct {
			Authority string `json:"authority"`
		}
		if err := json.Unmarshal([]byte(verified.Stdout), &answer); verified.Code != 0 || err != nil || answer.Authority != "sqlite" {
			t.Fatalf("verification: exit %d, %s, %s (%v)", verified.Code, verified.Stdout, verified.Stderr, err)
		}
	}
	if v, want := moveUserVersion(t, h, db), moveCurrentVersion(t, h); v != want {
		t.Errorf("follow store v%s; want %s", v, want)
	}
	if got := h.Sqlite(db, "SELECT count(*) FROM store_meta WHERE key='native_input_authority' AND value LIKE 'sqlite-v1:%';", "-readonly"); got != "1\n" {
		t.Errorf("follow authority: %q", got)
	}
	for _, copy := range env.Details.Copies {
		if restoreDescribe(t, copy.Copy) != beforeCopies[copy.Belongs] {
			t.Errorf("follow changed kept copy %s", copy.Copy)
		}
	}
	if tc.darwin {
		assertDarwinOnly(t, h)
	}
}
