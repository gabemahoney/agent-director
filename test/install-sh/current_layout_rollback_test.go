package installsh_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// b.ftd: follow current-layout rollback advice with the released v9 opener.
func TestAdviceFollow_CurrentLayoutRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the previous release and runs both installers")
	}
	doc, err := os.ReadFile("../../docs/operator-actions.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "## Going back to the previous release\n")
	if !ok {
		t.Fatal("rollback advice heading missing")
	}
	for _, copy := range []struct{ path, end string }{
		{"../../skills/install-agent-director/SKILL.md", ""},
		{"../../tickets/testplans/b.75s/t1.75s.f7/t2.75s.f7.wb/t2.75s.f7.wb.md", "\nEOF\n"},
	} {
		b, err := os.ReadFile(copy.path)
		if err != nil {
			t.Fatal(err)
		}
		_, got, found := strings.Cut(string(b), "## Going back to the previous release\n")
		if copy.end != "" {
			var complete bool
			got, _, complete = strings.Cut(got, copy.end)
			if !complete {
				t.Fatalf("%s: incomplete expected recovery text", copy.path)
			}
			got += "\n"
		}
		if !found || got != section {
			t.Fatalf("%s: complete recovery text differs from operator advice", copy.path)
		}
	}
	advice := strings.Join(strings.Fields(strings.Split(section, "\n1.")[0]), " ")
	for _, phrase := range []string{
		"including schema v9→v10, v10→v11, v11→v12, v12→v13, v13→v14 and v14→v15, the old-layout steps below do not apply",
		"stop agents, servers and callers and keep a copy of the actual",
		"store (`[store] db_path` when set), including its `-wal` and `-shm` files",
		"restore that pre-upgrade copy and install the previous release with its own",
		"`install.sh` and the original install flags",
		"removing a `-wal` or `-shm` that the copy lacks",
		"and the original install flags",
		"This loses all writes made since the backup",
		"The installer does not make this backup",
		"A copy made after migration cannot replace the pre-upgrade backup",
		"make a v15 store readable by a v9, v10, v11, v12, v13 or v14 binary",
		"There is no supported in-place downgrade from v15",
		"The tested v12→v11 family-history recipe applies only to a genuine family-only v12 store and cannot operate on v13, v14 or v15",
	} {
		if !strings.Contains(advice, phrase) {
			t.Fatalf("rollback advice changed; recheck the literal follow: missing %q", phrase)
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
			moveMustInstall(t, h)
			if v, current := h.migStoreVersion(db), moveCurrentVersion(t, h); v != current {
				t.Fatalf("upgrade schema = %s; want %s", v, current)
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
			rollbackRestoreStore(t, h, db, postUpgradeCopy)
			if got := h.Run(previous.Main, "list"); got.Code == 0 || !strings.Contains(got.Stderr, "ErrSchemaMismatch") {
				t.Fatalf("copy made after migration replaced the required pre-upgrade backup: %+v", got)
			}
			rollbackRestoreStore(t, h, db, backup)
			h.S, h.Pair = &old, previous
			moveMustInstall(t, h) // previous release's own installer, same flags
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

// Build the exact released baseline, rather than stamping today's binary v9.
func rollbackReleasedPair(t *testing.T) (movePair, string) {
	t.Helper()
	const revision = "ccb41e075695609b084292f52c36512f3665d210"
	src := t.TempDir()
	archive := exec.Command("git", "archive", revision)
	archive.Dir = "../.."
	b, err := archive.Output()
	if err != nil {
		t.Fatalf("archive released baseline: %v", err)
	}
	untar := exec.Command("tar", "-x", "-C", src)
	untar.Stdin = bytes.NewReader(b)
	if out, err := untar.CombinedOutput(); err != nil {
		t.Fatalf("extract released baseline: %v: %s", err, out)
	}
	pair := movePair{filepath.Join(src, "bin", "agent-director"), filepath.Join(src, "bin", "agent-director-admin")}
	for _, build := range []struct{ output, pkg string }{{pair.Main, "./cmd/agent-director"}, {pair.Admin, "./cmd/agent-director-admin"}} {
		flags := "-X " + moveVersPkg + ".Version=0.12.0 -X " + moveVersPkg + ".Commit=" + revision
		cmd := exec.Command("go", "build", "-ldflags", flags, "-o", build.output, build.pkg)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build released baseline: %v: %s", err, out)
		}
	}
	return pair, filepath.Join(src, "skills", "install-agent-director", "install.sh")
}
