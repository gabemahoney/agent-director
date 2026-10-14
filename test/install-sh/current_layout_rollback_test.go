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
	advice := strings.Join(strings.Fields(strings.Split(section, "\n1.")[0]), " ")
	for _, phrase := range []string{
		"including schema v9→v10, the old-layout steps below do not apply",
		"stop agents, servers and callers and keep a copy of the actual",
		"store (`[store] db_path` when set), including its `-wal` and `-shm` files",
		"restore that pre-upgrade copy and install the previous release with its own",
		"`install.sh` and the original install flags",
		"removing a `-wal` or `-shm` that the copy lacks",
		"and the original install flags",
		"This loses all writes made since the backup",
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
			if tc.companions {
				// Stop and wait for the only writer before copying committed WAL pages.
				h.HoldSQLite(db, "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;"+seed).Kill()
			} else {
				h.Sqlite(db, seed)
			}
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
			if tc.companions && (len(backup["-wal"]) == 0 || len(backup["-shm"]) == 0) {
				t.Fatal("the stopped writer must leave committed WAL and SHM to restore")
			}
			h.S, h.Pair = moveFixture(t), moveFixture(t).Current
			moveMustInstall(t, h)
			if v := h.migStoreVersion(db); v != "10" {
				t.Fatalf("upgrade schema = %s; want 10", v)
			}
			if got := h.Run(previous.Main, "list"); got.Code == 0 || !strings.Contains(got.Stderr, "ErrSchemaMismatch") {
				t.Fatalf("binary swap alone accepted v10: %+v", got)
			}
			// Stop the post-upgrade writer too; its WAL must not survive restore.
			h.HoldSQLite(db, "PRAGMA wal_autocheckpoint=0; UPDATE spawns SET cwd='/after', labels='{}' WHERE instance_id='before'; INSERT INTO spawns(instance_id,state,cwd,tmux_session_name,relay_mode,agent) VALUES ('after','ended','/after','ad-after','off','claude');").Kill()
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if b, exists := backup[suffix]; exists {
					h.Write(db+suffix, string(b), 0o600)
				} else if err := os.Remove(db + suffix); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
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
			if got := h.migRead(filepath.Join(h.New, "config.toml")); got != cfg {
				t.Fatalf("original settings changed: %q", got)
			}
			if kept := mustGlob(t, filepath.Join(h.Home, ".agent-director*")); len(kept) != 0 {
				t.Fatalf("current-layout recovery needs no old-layout directory: %v", kept)
			}
		})
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
