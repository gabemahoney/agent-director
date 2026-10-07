package hook_test

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// TestRelayConfigNegativeCapUsesDefault pins SR-11.2's negative-cap fallback
// (Epic AC #10): runRelay treats a PermissionRequestCap below 0, which
// config.Load does not refuse, as the default 1000. On a real store 1500 closed
// rows plus the new one are evicted down to 1000.
func TestRelayConfigNegativeCapUsesDefault(t *testing.T) {
	const id = "neg-cap-relay"
	st, dbPath := seedAgentRow(t, id, store.StateWorking)
	storefix.SeedClosedPermissionRequests(t, st, dbPath, id, 1500, time.Now().UTC().Add(-2*time.Hour), time.Second)
	now, restore := setupVirtualClock(t)
	defer restore()
	hc := hookConfig(envWith(id), agentParent(t, st, id))
	hc.Cfg, hc.Clock = config.Relay{TimeoutSeconds: 1, PermissionRequestCap: -1}, &advancingClock{now: now}

	if err := hook.Handle(context.Background(), strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`),
		io.Discard, st, hc, newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&count); err != nil || count != 1000 {
		t.Errorf("permission_requests rows = %d (%v); want 1000 (the default cap)", count, err)
	}
}
