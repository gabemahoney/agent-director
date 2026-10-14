package api_test

// b.w21: the real v6-to-current migration carries a decided permission
// request into a resumed launch; plain send-keys must remain usable.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestLegacyRecoveryMigratedV6Store(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	// Migration's proof uses the store's wall clock; the virtual clock stays within the 24-hour hold.
	e.clock.Advance(time.Now().UTC().Truncate(time.Second).Sub(e.clock.Now()))
	r := e.seedRow(t, killRowSpec{State: store.StateWaiting, RelayOn: true})
	path := filepath.Join(t.TempDir(), "state.db")
	fixture, err := os.ReadFile("../../test/install-sh/testdata/v0.11.1-store.sql")
	if err != nil {
		t.Fatal(err)
	}
	var targetVersion int
	legacyRecoveryRaw(t, e.dbPath, func(db *sql.DB) {
		if err := db.QueryRow("PRAGMA user_version").Scan(&targetVersion); err != nil {
			t.Fatal(err)
		}
	})
	token := storefix.TestRequestTokenA
	created := e.clock.Now().Add(-10 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	decided := e.clock.Now().Add(-9 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	legacyRecoveryRaw(t, path, func(db *sql.DB) {
		must := func(q string, args ...any) {
			t.Helper()
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
		}
		must(string(fixture))
		// These are the v5-to-v6 additions; the historical fixture owns all other DDL.
		must("ALTER TABLE spawns ADD COLUMN launch_owner_pid INTEGER")
		must("ALTER TABLE spawns ADD COLUMN launch_owner_starttime TEXT")
		must("ALTER TABLE spawns ADD COLUMN launch_owner_pidns TEXT")
		must("PRAGMA user_version = 6")
		must("UPDATE store_meta SET value = ? WHERE key = 'store_id'", e.storeID)
		must(`INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode, row_version)
			VALUES (?, 'ended', '/tmp', ?, 'on', 4)`, r.ID, r.Name)
		must(`INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input,
			decision, decision_reason, decided_at, created_at) VALUES (?, ?, 'Bash', '{}', 'allow', NULL, ?, ?)`,
			r.ID, token, decided, created)
		var version, modernColumns int
		if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('permission_requests')
			WHERE name IN ('hook_pid', 'settled_at', 'delivered_at', 'closed_at', 'proven_gone_at')`).Scan(&modernColumns); err != nil {
			t.Fatal(err)
		}
		if version != 6 || modernColumns != 0 {
			t.Fatalf("pre-migration version=%d, modern columns=%d; want genuine v6", version, modernColumns)
		}
	})
	data, err := json.Marshal(map[string]int{"from": 6, "to": targetVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "migrate-authorized"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Open(path)
	if err != nil {
		t.Fatalf("authorized store.Open v6-to-current: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	storefix.RegisterStorePath(t, migrated, path)
	e.dbPath, e.st, e.store.st = path, migrated, migrated
	legacyRecoveryRaw(t, path, func(db *sql.DB) {
		var version, modernColumns int
		if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != targetVersion {
			t.Fatalf("migrated version=%d; want fresh-store version %d", version, targetVersion)
		}
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('permission_requests')
			WHERE name IN ('hook_pid', 'settled_at', 'delivered_at', 'closed_at', 'proven_gone_at')`).Scan(&modernColumns); err != nil {
			t.Fatal(err)
		}
		if modernColumns != 5 {
			t.Fatalf("post-migration modern columns=%d; want all five", modernColumns)
		}
	})
	before := legacyRecoveryRead(t, migrated, r.ID, token)
	if !before.PreV7() || before.Hook != (store.ProcessIdentity{}) || !before.DeliveredAt.IsZero() ||
		before.PaneAnswer != store.PaneAnswerNone || before.ProvenGoneHow != store.ProvenGoneAgentGone || !before.ProvenGone() {
		t.Fatalf("migrated legacy request = %+v; want pre-v7 facts and migration's agent_gone proof", before)
	}
	if before.Decision != "allow" || before.DecisionReason != "" || before.DecidedAt.UTC().Format("2006-01-02 15:04:05") != decided ||
		before.CreatedAt.UTC().Format("2006-01-02 15:04:05") != created {
		t.Fatalf("migration changed legacy verdict/timestamps: %+v", before)
	}
	e.clock.Advance(before.ProvenGoneAt.Add(time.Second).Sub(e.clock.Now()))
	sp, err := migrated.GetSpawn(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	li := r.Spawn.Identity
	li.Token = newToken()
	res, version, err := migrated.MoveToPending(r.ID, sp.Snapshot, e.clock.Now().UnixMilli(), li.Token, r.Socket, "", store.LaunchOwner{})
	if err != nil || res != store.CondApplied {
		t.Fatalf("resume migrated spawn = %v, %v", res, err)
	}
	if res, err := migrated.RecordLaunchIdentity(r.ID, version, li.Token, li); err != nil || res != store.CondApplied {
		t.Fatalf("record resumed launch = %v, %v", res, err)
	}
	r.Token, r.Spawn.Identity = li.Token, li
	if err := e.rec.SetLabel(r.Socket, r.Session.ID, li.PaneID, li.Token, r.ID, r.StoreID); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{store.StatePending, store.StateWaiting} {
		if state == store.StateWaiting {
			if applied := apitest.ApplyAgentHook(t, path, r.ID, "SessionStart", "sess-"+newToken()); !applied.Applied {
				t.Fatalf("migrated resumed SessionStart = %+v; want applied", applied)
			}
		}
		r.Spawn.State = state
		e.rec.Reset()
		legacyRecoveryAssertTyped(t, e, r, legacyRecoverySend(e, r, e.clock.Now(), ""))
		legacyRecoveryAssertVerdict(t, before, legacyRecoveryRead(t, migrated, r.ID, token))
	}
}
