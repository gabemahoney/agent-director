package api_test

// store_busy_timeout_test.go pins b.c7f at the Client: api.New opens the store
// with the loaded config's [store] busy_timeout_ms, whichever tier names it.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestNewOpensStoreWithConfiguredBusyTimeout is the b.c7f regression: under a held
// write lock a delete gives up at once with 1 ms, and waits for the release with 60000.
func TestNewOpensStoreWithConfiguredBusyTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		ms        int
		storePath bool          // Options.StorePath and CreateIfMissing; otherwise [store] db_path, an existing store
		hold      time.Duration // the lock's release, unless the delete returns first
		want      string
	}{
		{"1 ms, store path option", 1, true, 2 * time.Second, "ErrInternal"},
		{"1 ms, [store] db_path", 1, false, 2 * time.Second, "ErrInternal"},
		{"60000 ms waits for the release", 60000, true, 300 * time.Millisecond, "ErrSpawnNotFound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dbPath, cfgPath := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.toml")
			cfg := fmt.Sprintf("[store]\nbusy_timeout_ms = %d\n", tc.ms)
			opts := api.Options{ConfigPath: cfgPath, CreateIfMissing: true}
			if tc.storePath {
				opts.StorePath = dbPath
			} else {
				cfg += fmt.Sprintf("db_path = %q\n", dbPath)
				if _, err := apitest.InitStore(dbPath); err != nil {
					t.Fatalf("InitStore: %v", err)
				}
				opts.CreateIfMissing = false
			}
			if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			c, err := api.New(opts)
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })

			release := apitest.HoldWriteLock(t, dbPath, tc.hold)
			res, err := adminapi.Delete(c, []string{"no-such-row"})
			release()

			if err != nil || res.Results["no-such-row"] != tc.want {
				t.Errorf("delete under the held lock = %v, %v; want %s", res.Results, err, tc.want)
			}
		})
	}
}
