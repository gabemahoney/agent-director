package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.51h, CR-3: every copy must undergo its value parser before a later
// help/unknown token or the successful-parse repeat guard can win.
func TestB51hMalformedRepeatedValues(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range []struct {
		verb, valid, invalid, want string
	}{
		{"list", "--limit=1", "--limit=bad", `invalid value "bad" for flag -limit: not a base-10 integer`},
		{"send-keys", "--force", "--force=bad", `invalid boolean value "bad" for -force: not a boolean`},
		{"decide", "--max-wait-ms=1", "--max-wait-ms=bad", `invalid value "bad" for flag -max-wait-ms: not a base-10 integer`},
	} {
		for _, goodCopies := range []int{1, 2} {
			for _, suffix := range []string{"", "--help", "--unknown"} {
				t.Run(fmt.Sprintf("%s/good-copies-%d/then-%s", tc.verb, goodCopies, suffix), func(t *testing.T) {
					home := t.TempDir()
					id, err := apitest.SeedSpawn(stateDB(home), "", "ended", "", "", "", true)
					if err != nil {
						t.Fatal(err)
					}
					before, err := apitest.ReadSpawnColumns(stateDB(home), id)
					if err != nil {
						t.Fatal(err)
					}
					args := []string{tc.verb}
					for i := 0; i < goodCopies; i++ {
						args = append(args, tc.valid)
					}
					args = append(args, tc.invalid)
					if suffix != "" {
						args = append(args, suffix)
					}
					stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
					if got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; got != tc.want {
						t.Errorf("description=%q; want %q", got, tc.want)
					}
					// Base initializes the client before flag parsing; decide also
					// audits the attempt. Verify no command action on the seeded row.
					after, err := apitest.ReadSpawnColumns(stateDB(home), id)
					if err != nil || !reflect.DeepEqual(after, before) {
						t.Errorf("row changed: %+v (%v); want %+v", after, err, before)
					}
					for _, name := range []string{filepath.Join(directorDir(home), "templates"), filepath.Join(home, "fake-tmux.log")} {
						if _, err := os.Lstat(name); !os.IsNotExist(err) {
							t.Errorf("parse error wrote %s: %v", name, err)
						}
					}
				})
			}
		}
	}
}

// b.51h: validating all copies must retain clean-repeat refusal before setup.
func TestB51hCleanRepeatValueControl(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runSpawnCLI(t, home, buildFakeTmux(t), "get", "--instance-id=A", "--instance-id=B")
	if got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; got != "flag --instance-id may only be specified once" {
		t.Errorf("description=%q; want clean repeat refusal", got)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 1 || entries[0].Name() != "tmux-tmpdir" {
		t.Errorf("clean repeat wrote under HOME: %v (%v)", entries, err)
	}
}
