package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/dbpathfix"
)

// TestInstallShDbPathMatchesGo is the b.2io drift guard: for every config and
// HOME in dbpathfix, install.sh's [store] db_path reader names the store the
// binary opens (config.Load, then resolveStorePath), or refuses the file.
func TestInstallShDbPathMatchesGo(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	for i, c := range dbpathfix.Cases {
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, home := range dbpathfix.Homes {
			t.Run(fmt.Sprintf("%s/HOME=%q", c.Name, home), func(t *testing.T) {
				t.Setenv("HOME", home)
				got := r.Resolve(t, cfg, home)
				loaded, err := config.Load(cfg)
				want := ""
				if err == nil {
					want, err = resolveStorePath("", loaded)
				}
				switch c.Expect {
				case dbpathfix.Same:
					if err != nil {
						t.Fatalf("Go refuses a config the matrix expects it to read: %v", err)
					}
					if got.Code != 0 || got.Stdout != want+"\n" || got.Stderr != "" {
						t.Errorf("reader: status %d, stdout %q, stderr %q; want status 0 and Go's store path %q",
							got.Code, got.Stdout, got.Stderr, want)
					}
				case dbpathfix.ReaderRefuses:
					head, _, _ := strings.Cut(got.Stderr, "\n")
					if got.Code != 1 || got.Stdout != "" ||
						head != "install.sh: cannot tell which store database agent-director opens; refusing to install." {
						t.Errorf("reader: status %d, stdout %q, stderr %q; want a refusal (status 1, nothing on stdout)",
							got.Code, got.Stdout, got.Stderr)
					}
				case dbpathfix.GoRefuses:
					if err == nil {
						t.Errorf("config.Load accepts the config (store %q); the matrix expects it refused", want)
					}
					if got.Code != 0 {
						t.Errorf("reader: status %d, stderr %q; want it to pass the file to the binary", got.Code, got.Stderr)
					}
				}
			})
		}
	}
}
