package installsh_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b.ftd: follow current-layout rollback advice with the released v9 opener.
func TestAdviceFollow_CurrentLayoutRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the previous release and runs both installers")
	}
	const skillPath = "../../skills/install-agent-director/SKILL.md"
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	_, pointer, ok := strings.Cut(string(skill), "## Going back to the previous release\n")
	if !ok {
		t.Fatal("install skill recovery pointer missing")
	}
	_, link, ok := strings.Cut(pointer, "Follow [Going back to the previous release](")
	if !ok {
		t.Fatal("install skill recovery link missing")
	}
	target, _, ok := strings.Cut(link, ")")
	const sourceURL = "https://github.com/gabemahoney/agent-director/blob/main/"
	if !ok || target != sourceURL+"docs/operator-actions.md#going-back-to-the-previous-release" {
		t.Fatalf("install skill recovery link changed: %q", target)
	}
	// The absolute URL also works beside a standalone install.sh. Resolve its
	// repository path in the frozen source so the offline test follows this
	// candidate's canonical procedure, rather than an unrelated deployed version.
	doc, err := os.ReadFile(filepath.Join("../..", strings.TrimPrefix(strings.Split(target, "#")[0], sourceURL)))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "## Going back to the previous release\n")
	if !ok {
		t.Fatal("rollback advice heading missing")
	}
	const expectedPath = "../../tickets/testplans/b.75s/t1.75s.f7/t2.75s.f7.wb/t2.75s.f7.wb.md"
	b, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	_, got, found := strings.Cut(string(b), "## Going back to the previous release\n")
	got, _, complete := strings.Cut(got, "\nEOF\n")
	if !found || !complete || got+"\n" != section {
		t.Fatalf("%s: complete recovery text differs from linked operator advice", expectedPath)
	}
	advice := strings.Join(strings.Fields(strings.Split(section, "\n1.")[0]), " ")
	for _, phrase := range []string{
		"including schema v9→v10 through v19→v20, the old-layout steps below do not apply",
		"stop all writers and keep a copy of the actual store (`[store] db_path` when set), its existing `-wal`/`-shm`, and all legacy input evidence",
		"Keep provider logs/transcripts outside those roots too",
		"maintain the same exclusion, preserve the post-upgrade evidence",
		"restore the matching pre-upgrade DB and input evidence at their original paths",
		"removing a `-wal` or `-shm` that the copy lacks",
		"Keep original launch-lock inodes; do not replace or delete them",
		"Install the matching previous release with its own `install.sh` and the original flags",
		"omitting `--schedule` to preserve a custom cadence",
		"Before restarting any older runtime, restore `find-missing` as the recovery command in every schedule that runs `sweep`",
		"the tagged cron line (`crontab -e`), the service `ExecStart`, or a schedule you made another way",
		"keeping your chosen cadence and environment",
		"After editing a service, run `systemctl --user daemon-reload`",
		"Then reconcile uncertain attempts against the preserved evidence",
		"restoration loses all later writes and can lose proof of input that already reached an agent",
		"Never replay an attempt merely because the restored copy lacks its proof",
		"The journal cutover creates a separate stopped-writer backup before schema changes; other installer steps do not supply this complete recovery set",
		"A copy made after migration cannot replace the pre-upgrade set",
		"Schema v20 display evidence has no supported reverse recipe",
		"Schema v19 confirmation evidence has no supported reverse recipe",
		"Schema v18 Cursor input and proof evidence has no supported reverse recipe",
		"Schema v17 permission provenance and response claims have no supported reverse recipe",
		"Schema v16 has no in-place downgrade or replayable file-journal export",
		"A binary swap or version reset cannot undo these changes",
		"Schema v15, v14 and v13 are also restore-only",
		"The v12→v11 family-history recipe applies only to genuine family-only v12 stores; it cannot operate on v13–v20",
		"Without that set, or while an uncertain attempt remains unreconciled, stop and obtain human help",
	} {
		if !strings.Contains(advice, phrase) {
			t.Fatalf("rollback advice changed; recheck the literal follow: missing %q", phrase)
		}
	}
	// b.x9o: the migration guide's v20 extension prescribes this same
	// stopped-writer restore, exercised below against the current binary.
	migration, err := os.ReadFile("../../docs/migration-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{
		"**Current recovery policy.** Schemas v20, v19, v18, v17 and v16 are restore-only.",
		"The v20 display columns and v19 evidence columns have no reverse recipe: restore the stopped-writer pre-upgrade set, losing all later writes, and never drop the columns or reset the version.",
		"This illustrates the historical pattern, not a recipe for v9 through v20. Never merely reset the version pragma.",
		"recipe below is only for genuine family-only v12, never v13–v20 or a hand-reset store.",
	} {
		if !strings.Contains(strings.Join(strings.Fields(string(migration)), " "), phrase) {
			t.Fatalf("migration recovery advice changed; recheck the literal follow: missing %q", phrase)
		}
	}
	previous, script := rollbackReleasedPair(t)
	for _, tc := range []struct {
		name                 string
		external, companions bool
	}{
		{"default/checkpointed", false, false},
		{"default/uncheckpointed", false, true},
		{"external/checkpointed", true, false},
		{"external/uncheckpointed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMoveHome(t)
			db := filepath.Join(h.New, "state.db")
			cfg := "[defaults]\ndisable_ask_user = true\n[store]\nbusy_timeout_ms = 5000\n"
			if tc.external {
				db = filepath.Join(h.Home, "data", "state.db")
				cfg += "db_path = \"" + db + "\"\n"
			}
			h.Write(filepath.Join(h.New, "config.toml"), cfg, 0o600)
			if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
				t.Fatal(err)
			}
			old := *h.S
			old.Install = script
			h.S, h.Pair = &old, previous
			moveMustInstall(t, h)
			if v := h.migStoreVersion(db); v != "9" {
				t.Fatalf("released baseline created schema %s; want 9", v)
			}
			seed := "INSERT INTO spawns(instance_id,state,cwd,tmux_session_name,relay_mode,agent,agent_args,labels,session_id) VALUES ('before','ended','/before','ad-before','off','codex','[\"-m\",\"original\"]','{\"owner\":\"before\"}','original-session');"
			seed += "UPDATE spawns SET launch_token='0123456789abcdef',life_number=2,row_version=7,pid=4242,proc_starttime='original-process',pane_id='%4',pane_pid=4242,pane_starttime='original-pane' WHERE instance_id='before';"
			seed += "INSERT INTO session_history(instance_id,session_id,jsonl_path,life_number) VALUES('before','previous-session','/before.jsonl',1);"
			seed += "INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,hook_gone_at,pane_answer,pane_as,decision,decision_reason,proven_gone_at,proven_gone_how) VALUES('before','aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','Bash','{}','2026-10-19 12:00:01',1792411205000,1792411206000,'outside','deny','deny','pane_outside',1792411207000,'agent_gone');"
			if tc.companions {
				// Stop and wait for the only writer before copying committed WAL pages.
				h.HoldSQLite(db, "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;"+seed).Kill()
			} else {
				h.Sqlite(db, seed)
			}
			backup := rollbackStoreCopy(t, db)
			if tc.companions && (len(backup["-wal"]) == 0 || len(backup["-shm"]) == 0) {
				t.Fatal("the stopped writer must leave committed WAL and SHM to restore")
			}
			preservedQuery := "SELECT launch_token,life_number,row_version,pid,proc_starttime,pane_id,pane_pid,pane_starttime FROM spawns; SELECT * FROM permission_requests; SELECT * FROM session_history; SELECT * FROM store_meta;"
			preserved := h.Sqlite(db, preservedQuery)
			h.S, h.Pair = moveFixture(t), moveFixture(t).Current
			originalFlags := append([]string(nil), h.Flags...)
			h.authorizeJournalMigration()
			moveMustInstall(t, h)
			if v, current := h.migStoreVersion(db), moveCurrentVersion(t, h); v != current {
				t.Fatalf("upgrade schema = %s; want %s", v, current)
			}
			if got := h.Sqlite(db, "SELECT name FROM pragma_table_info('spawns') WHERE name IN ('state_evidence','settled_evidence','process_evidence','display_evidence') ORDER BY name;"); got != "display_evidence\nprocess_evidence\nsettled_evidence\nstate_evidence\n" {
				t.Fatalf("current upgrade lacks confirmation columns: %q", got)
			}
			if got := h.Sqlite(db, "SELECT name FROM pragma_table_info('spawns') WHERE name IN ('codex_startup','claude_capacity') ORDER BY name;"); got != "claude_capacity\ncodex_startup\n" {
				t.Fatalf("v13 upgrade lacks both provenance columns: %q", got)
			}
			if got := h.Sqlite(db, "SELECT count(*) FROM pragma_table_info('spawns') WHERE name IN ('permission_epoch','observation_version','display_observation','display_exit','idle_screen_timeout_seconds','pane_sha256','pane_since_ms','pane_epoch'); SELECT count(*) FROM pragma_table_info('permission_requests') WHERE name IN ('owner_launch_token','owner_permission_epoch','admitted_at','fallback_at','fallback_basis');"); got != "8\n5\n" {
				t.Fatalf("v14 upgrade lacks permission observation/ownership or pane columns: %q", got)
			}
			if got := h.Sqlite(db, "SELECT name FROM pragma_table_info('spawns') WHERE name IN ('codex_turn','codex_turn_ended') ORDER BY name;"); got != "codex_turn\ncodex_turn_ended\n" {
				t.Fatalf("v15 upgrade lacks both Codex turn columns: %q", got)
			}
			if got := h.Run(previous.Main, "list"); got.Code == 0 || !strings.Contains(got.Stderr, "ErrSchemaMismatch") {
				t.Fatalf("binary swap alone accepted the current schema: %+v", got)
			}
			postUpgradeCopy := rollbackStoreCopy(t, db)
			// Stop the post-upgrade writer too; its WAL must not survive restore.
			h.HoldSQLite(db, "PRAGMA wal_autocheckpoint=0; UPDATE spawns SET cwd='/after', labels='{}' WHERE instance_id='before'; INSERT INTO spawns(instance_id,state,cwd,tmux_session_name,relay_mode,agent) VALUES ('after','ended','/after','ad-after','off','claude');").Kill()
			// Preserve the later evidence before either restore. Inspect a separate
			// copy so SQLite cannot checkpoint or remove its saved WAL/SHM.
			postUpgradeEvidence := rollbackStoreCopy(t, db)
			inspection := filepath.Join(t.TempDir(), "state.db")
			rollbackRestoreStore(t, h, inspection, postUpgradeEvidence)
			if got := h.Sqlite(inspection, "SELECT instance_id,cwd FROM spawns ORDER BY instance_id; SELECT count(*) FROM input_attempts; SELECT count(*) FROM input_launches;"); got != "after|/after\nbefore|/after\n0\n0\n" {
				t.Fatalf("preserved evidence must retain later writes and establish no input to reconcile before older runtime use: %q", got)
			}
			rollbackRestoreStore(t, h, db, postUpgradeCopy)
			if got := h.Run(previous.Main, "list"); got.Code == 0 || !strings.Contains(got.Stderr, "ErrSchemaMismatch") {
				t.Fatalf("copy made after migration replaced the required pre-upgrade backup: %+v", got)
			}
			rollbackRestoreStore(t, h, db, backup)
			h.S, h.Pair = &old, previous
			h.Flags = originalFlags
			moveMustInstall(t, h) // previous release's own installer, same flags
			// b.x9o: restored schedules must use the older binary's recovery verb.
			// Cadence and environment are operator edits, not installer defaults.
			restored := filepath.Join(h.New, "bin", "agent-director")
			if got := h.Run(restored, "find-missing"); got.Code != 0 {
				t.Fatalf("restored previous release cannot run scheduled recovery: %+v", got)
			}
			got := h.Run(restored, "sweep")
			var refusal struct {
				ErrName string `json:"err_name"`
			}
			if err := json.Unmarshal([]byte(got.Stderr), &refusal); err != nil || got.Code == 0 || refusal.ErrName != "ErrUnknownVerb" {
				t.Fatalf("restored previous release must refuse sweep as unknown: %+v (decode: %v)", got, err)
			}
			if got := h.Run(filepath.Join(h.New, "bin", "agent-director"), "get", "--instance-id", "before"); got.Code != 0 || !strings.Contains(got.Stdout, "original-session") || !strings.Contains(got.Stdout, "/before") {
				t.Fatalf("restored previous release cannot read original row: %+v", got)
			}
			if v := h.migStoreVersion(db); v != "9" {
				t.Fatalf("rollback version = %s; want 9", v)
			}
			want := "before|/before|codex|[\"-m\",\"original\"]|{\"owner\":\"before\"}|original-session\n"
			if rows := h.Sqlite(db, "SELECT instance_id,cwd,agent,agent_args,labels,session_id FROM spawns ORDER BY instance_id;"); rows != want {
				t.Fatalf("restored rows = %q; want %q (post-upgrade writes lost)", rows, want)
			}
			if got := h.Sqlite(db, preservedQuery); got != preserved {
				t.Fatalf("restore changed identity, life/history, permission answers/proof/timing or store identity: got %q; want %q", got, preserved)
			}
			if got := h.migRead(filepath.Join(h.New, "config.toml")); got != cfg {
				t.Fatalf("original settings changed: %q", got)
			}
			if kept := mustGlob(t, filepath.Join(h.Home, ".agent-director*")); len(kept) != 0 {
				t.Fatalf("current-layout recovery needs no old-layout directory: %v", kept)
			}
		})
	}
}

func rollbackStoreCopy(t *testing.T, db string) map[string][]byte {
	t.Helper()
	backup := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(db + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		backup[suffix] = b
	}
	return backup
}

func rollbackRestoreStore(t *testing.T, h *moveHome, db string, backup map[string][]byte) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if b, exists := backup[suffix]; exists {
			h.Write(db+suffix, string(b), 0o600)
		} else if err := os.Remove(db + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// Build the exact released baseline (v0.12.0's commit, schema v9) with the
// genuine-release fixture, rather than stamping today's binary v9. A missing
// revision fails the test.
func rollbackReleasedPair(t *testing.T) (movePair, string) {
	t.Helper()
	r, err := releaseBuildOf("ccb41e075695609b084292f52c36512f3665d210")
	if err != nil {
		t.Fatalf("released baseline: %v", err)
	}
	return r.Pair, r.Install
}
