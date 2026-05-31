package hook_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// TestRelayConfigNegativeCapUsesDefault pins SR-11.2's negative-cap fallback and
// Epic AC #10. A PermissionRequestCap < 0 in config.Relay silently falls back to
// the default of 1000 at the runRelay call site (internal/hook/permission.go:108-116).
//
// This mirrors the TimeoutSeconds <= 0 guard at internal/hook/polling.go:89-94
// (introduced for b.p48): both are silent fallbacks to the production-safe default
// when the configured value is out of the valid positive range.
func TestRelayConfigNegativeCapUsesDefault(t *testing.T) {
	for _, negativeCap := range []int{-1, -1000} {
		negativeCap := negativeCap
		t.Run(fmt.Sprintf("cap_%d", negativeCap), func(t *testing.T) {
			// Use a real SQLite store — a mock cannot verify that actual
			// eviction happened (which requires the store to have executed
			// the DELETE).
			s, dbPath := storefix.OpenTempStore(t)

			const instanceID = "neg-cap-relay"
			// Seed 1500 closed rows — above the default cap of 1000.
			// SeedClosedPermissionRequests creates the spawn row if absent.
			base := time.Now().UTC().Add(-2 * time.Hour)
			storefix.SeedClosedPermissionRequests(t, s, dbPath, instanceID, 1500, base, time.Second)

			// Virtual clock forces the polling loop to timeout instantly,
			// so the test costs zero wall-clock time. Mirrors the approach
			// in TestFailClosedPollingTimeout and TestFailClosedTimeoutWritesDBBeforeStdout.
			now, restore := setupVirtualClock(t)
			defer restore()
			clock := &advancingClock{now: now}

			var stdout bytes.Buffer
			cfg := config.Relay{
				TimeoutSeconds: 1,
				PollBaseMs:     0,
				PollJitterMs:   0,
				// The runRelay call site applies: cap < 0 → fallback to 1000.
				// Mirror of internal/hook/polling.go:89-94 TimeoutSeconds <= 0 guard.
				PermissionRequestCap: negativeCap,
			}

			if err := hook.Handle(context.Background(),
				strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`),
				&stdout, s,
				hook.HandleConfig{Env: envWith(instanceID), Cfg: cfg, Clock: clock},
				newSilentLogger()); err != nil {
				t.Fatalf("Handle (cap=%d): %v", negativeCap, err)
			}

			// Post-call row count must be 1000:
			//   1500 seeded closed + 1 new open (upsert) = 1501 total
			//   → 501 evicted (default cap=1000 applied via fallback)
			//   → 1000 rows remain.
			// The timeout path then decides the open row deny/timeout → still 1000.
			raw, err := sql.Open("sqlite", "file:"+dbPath)
			if err != nil {
				t.Fatalf("open raw db: %v", err)
			}
			defer func() { _ = raw.Close() }()

			var count int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&count); err != nil {
				t.Fatalf("count rows: %v", err)
			}
			if count != 1000 {
				t.Errorf("cap=%d: post-call row count = %d; want 1000 (default cap applied via call-site fallback at internal/hook/permission.go:108-116)",
					negativeCap, count)
			}
		})
	}
}
