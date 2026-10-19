package api_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestFamilyMembershipAndReadScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, parent, child, leaf, relay, want string }{
		{"lone waiting", "waiting", "", "", "", "waiting"},
		{"lone working", "working", "", "", "", "working"},
		{"grandchild working", "waiting", "waiting", "working", "", "working"},
		{"all waiting", "waiting", "waiting", "waiting", "", "waiting"},
		{"pending descendant", "waiting", "pending", "", "", "working"},
		{"ask user descendant", "waiting", "ask_user", "", "", "working"},
		{"legacy permission descendant", "waiting", "check_permission", "", "off", "working"},
		{"relay permission descendant", "waiting", "check_permission", "", "on", "working"},
		{"ended intermediate", "waiting", "ended", "working", "", "working"},
		{"missing intermediate", "waiting", "missing", "working", "", "working"},
		{"ended member excluded", "waiting", "ended", "", "", "waiting"},
		{"missing member excluded", "waiting", "missing", "", "", "waiting"},
		{"no live members", "ended", "missing", "ended", "", "waiting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "state.db")
			seed := func(id, state, relay string) {
				if _, err := apitest.SeedSpawn(dbPath, id, state, "/family/"+id, relay, "", true); err != nil {
					t.Fatal(err)
				}
			}
			seed("parent", tc.parent, "")
			if tc.child != "" {
				seed("child", tc.child, tc.relay)
				if tc.child == store.StateCheckPermission {
					if _, err := apitest.SeedPermissionRequest(dbPath, "child", "Bash"); err != nil {
						t.Fatal(err)
					}
				}
				if err := apitest.SeedParentChild(dbPath, "parent", "child"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.leaf != "" {
				seed("leaf", tc.leaf, "")
				if err := apitest.SeedParentChild(dbPath, "child", "leaf"); err != nil {
					t.Fatal(err)
				}
			}
			c := settleClientFor(t, dbPath, 0, tmuxfix.NewRecorder(), time.Now)
			status, row, listed := clientReported(t, c, "parent")
			if status.FamilyState != tc.want || row.FamilyState != tc.want || listed.FamilyState != tc.want {
				t.Fatalf("family status/get/list=%q/%q/%q; want %q", status.FamilyState, row.FamilyState, listed.FamilyState, tc.want)
			}
			if status.State != tc.parent || len(row.PermissionRequests) != 0 || len(listed.PermissionRequests) != 0 || len(row.UnprovenRequests) != 0 {
				t.Fatalf("descendant changed ancestor's own result: %+v %+v", status, row)
			}
			for _, filter := range []api.ListParams{{Cwd: "/family/parent"}, {State: []string{tc.parent}, Limit: 1}, {TmuxSessionName: "ts-parent", Limit: 1}} {
				res, err := c.List(filter)
				if err != nil || len(res.Spawns) != 1 || res.Spawns[0].InstanceID != "parent" || res.Spawns[0].FamilyState != tc.want {
					t.Fatalf("filtered family: %+v %v", res, err)
				}
			}
		})
	}
}

func TestFamilyClientReopenUsesDurableIndependentHistory(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	clock := tmuxfix.NewClock(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if _, err := apitest.SeedSpawn(dbPath, "family-reopen", "working", "", "", "", true, apitest.WithSettle("waiting", clock.Now().UnixMilli()+settleMax)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetFamilyHistory(dbPath, "family-reopen", "working", "waiting", clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), clock.Now)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Duration(settleMax) * time.Millisecond)
	reopened := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), clock.Now)
	status, row, listed := clientReported(t, reopened, "family-reopen")
	if status.State != "waiting" || status.FamilyState != "working" || row.FamilyState != "working" || listed.FamilyState != "working" {
		t.Fatalf("independent reopened reports: %+v %+v %+v", status, row, listed)
	}
}

func TestFamilyCursorStoredMembershipWhileWriterHeld(t *testing.T) {
	// Serial: deadline-sensitive held writer fixture runs outside the parallel wave.
	e, c, b, _ := cursorReadLaunch(t)
	if _, err := apitest.SeedSpawn(e.dbPath, "family-parent", "waiting", "/family-parent", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, "family-parent", b.InstanceID); err != nil {
		t.Fatal(err)
	}
	e.pc.Set(b.GatewayPID, procfix.Gone())
	e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	defer release()
	start := time.Now()
	status, err := c.Status(b.InstanceID)
	if err != nil || status.State != "missing" || status.FamilyState != "working" {
		t.Fatalf("projected missing discarded stored live member: %+v %v", status, err)
	}
	parent, row, listed := clientReported(t, c, "family-parent")
	if parent.State != "waiting" || parent.FamilyState != "working" || row.FamilyState != "working" || listed.FamilyState != "working" {
		t.Fatalf("stored descendant excluded: %+v %+v %+v", parent, row, listed)
	}
	res, err := c.List(api.ListParams{Cwd: "/family-parent", Limit: 1})
	if err != nil || len(res.Spawns) != 1 || res.Spawns[0].FamilyState != "working" {
		t.Fatalf("filtered stored family=%+v %v", res, err)
	}
	if time.Since(start) > time.Second {
		t.Error("family reads waited for writer lock")
	}
	if got := e.columns(t, b.InstanceID); got.State == "missing" {
		t.Fatal("contended projection persisted terminal state")
	}
}
