package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/dbpathfix"
)

// TestInstallShSentinelPathMatchesStore (b.2io): for every store path
// install.sh's reader resolves from the dbpathfix matrix, the sentinel it
// writes (ad_sentinel_path) is the one this store looks for (sentinelPath).
func TestInstallShSentinelPathMatchesStore(t *testing.T) {
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	for i, c := range dbpathfix.Cases {
		if c.Expect != dbpathfix.Same {
			continue
		}
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, home := range dbpathfix.Homes {
			t.Run(fmt.Sprintf("%s/HOME=%q", c.Name, home), func(t *testing.T) {
				res := r.Resolve(t, cfg, home)
				if res.Code != 0 {
					t.Fatalf("reader: status %d, stderr %q", res.Code, res.Stderr)
				}
				db := strings.TrimSuffix(res.Stdout, "\n")
				if got, want := r.Sentinel(t, db), sentinelPath(db); got != want {
					t.Errorf("ad_sentinel_path %q = %q; the store looks for %q", db, got, want)
				}
			})
		}
	}
}
