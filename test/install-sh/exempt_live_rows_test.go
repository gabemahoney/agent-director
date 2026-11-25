package installsh_test

// b.kjv: direct exempt upgrades must refuse before store recovery can clear
// live rows; file-only upgrades must keep accepting them without store work.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeLiveRows(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.18.8")
	}
	r := genuineRelease(t, "v0.18.8")
	for _, name := range []string{"exempt-live-rows", "exempt-unreadable-rows", "file-only-live-rows"} {
		t.Run(name, func(t *testing.T) {
			h := newMoveHome(t)
			bin := h.InstallRelease(r)
			db := filepath.Join(h.New, "state.db")
			if got := moveUserVersion(t, h, db); got != "20" {
				t.Fatalf("v0.18.8 store version = %q; want 20", got)
			}
			for _, entry := range []string{"install-contract.json", "current", "versions"} {
				if _, err := os.Lstat(filepath.Join(h.New, entry)); !os.IsNotExist(err) {
					t.Fatalf("unexpected layout entry %s: %v", entry, err)
				}
			}
			if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("v0.18.8 binary is not a regular file: %v", err)
			}
			if name == "file-only-live-rows" {
				moveMustInstall(t, h) // establish the layout with no live rows
				if _, err := os.Lstat(filepath.Join(h.New, "current")); err != nil {
					t.Fatalf("no versioned layout after exempt install: %v", err)
				}
			}
			h.Sqlite(db, crossingLiveRowSQL("a-waiting", "waiting")+crossingLiveRowSQL("b-working", "working"))
			if name == "exempt-unreadable-rows" {
				h.Sqlite(db, "ALTER TABLE spawns RENAME COLUMN state TO state_gone;")
			}
			version := moveUserVersion(t, h, db)
			dump := h.Sqlite(db, ".dump", "-readonly")
			before := h.PhysicalSnapshot()
			res := h.Install() // run directly, without the upgrade skill's stop/sweep

			if name == "file-only-live-rows" {
				if res.Code != 0 {
					t.Fatalf("file-only install: exit %d\n%s\n%s", res.Code, res.Stdout, res.Stderr)
				}
				if !strings.Contains(res.Stdout, "schema  : not inspected (file-only upgrade)\n") {
					t.Errorf("file-only install did not report its store exclusion:\n%s", res.Stdout)
				}
			} else {
				assertRefusedUnchanged(t, h, before)
				for _, entry := range []string{"install-contract.json", "current", "versions"} {
					if _, err := os.Lstat(filepath.Join(h.New, entry)); !os.IsNotExist(err) {
						t.Errorf("refusal published %s: %v", entry, err)
					}
				}
				if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() {
					t.Errorf("refusal replaced the regular binary: %v", err)
				}
			}
			if got := moveUserVersion(t, h, db); got != version {
				t.Errorf("store version after install = %q; want %q", got, version)
			}
			if got := h.Sqlite(db, ".dump", "-readonly"); got != dump {
				t.Error("install changed the store dump")
			}
			state := "state"
			if name == "exempt-unreadable-rows" {
				state = "state_gone"
			}
			if got := h.Sqlite(db, "SELECT instance_id || ' ' || "+state+" FROM spawns ORDER BY instance_id;", "-readonly"); got != "a-waiting waiting\nb-working working\n" {
				t.Errorf("rows after install:\n%s\nwant both rows still live", got)
			}
			if name == "file-only-live-rows" {
				return
			}

			reason := "agents_live"
			action := "stop each live agent with " + bin + " pause --instance-id <id> (a waiting row) or " + bin +
				" kill --instance-id <id> (any other live row); only for an agent you have established is gone, clear its row with " + bin +
				" find-missing, run as the agents' user in their tmux environment (a missing row does not prove the agent stopped: docs/maintenance.md \"Scheduling the sweeps\"); then re-run the same install command from a shell AD did not start"
			if name == "exempt-unreadable-rows" {
				reason = "agent_rows_unreadable"
				action = "re-run the same install command once state.db is no longer busy (a re-run reads its rows again); if it still cannot be read, inspect " + db + " with the error this run reported"
				if !strings.Contains(res.Stderr, "no such column: state\n") {
					t.Errorf("refusal lacks sqlite3's error:\n%s", res.Stderr)
				}
			}
			envs := preflightEnvelopes(t, res, "ErrUpgradeRefused")
			assertOutcomeEnvelope(t, envs[len(envs)-1], "ErrUpgradeRefused", reason, action, "not_committed", nil)
		})
	}
}
