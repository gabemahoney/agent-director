package main_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.t1b: delete's value-taking flag owns global-looking ids; both flag forms
// remove the requested rows from HOME's store without creating a decoy tree.
func TestAdminGlobalSpellingsInFlagValues(t *testing.T) {
	for _, global := range []string{"--store-path", "--home", "--tmux-command", "--create-if-missing"} {
		for _, equals := range []bool{false, true} {
			form := "two-token"
			if equals {
				form = "equals"
			}
			t.Run(global+"/"+form, func(t *testing.T) {
				home, sentinel, _ := seedRow(t, store.StateEnded, "")
				decoy := filepath.Join(t.TempDir(), "decoy")
				value := global + "=" + decoy
				if global == "--store-path" {
					value = global + "=" + filepath.Join(decoy, "state.db")
				} else if global == "--create-if-missing" {
					value = global + "=false"
				}
				id, err := apitest.SeedSpawn(stateDB(home), value, store.StateEnded, "", "", "", false)
				if err != nil {
					t.Fatalf("seed global-looking id: %v", err)
				}
				args := []string{"delete", "--instance-id", id}
				if equals {
					args = []string{"delete", "--instance-id=" + id}
				}
				// This second id also proves the scanner does not steal the
				// following flag when it encounters a global-looking value.
				args = append(args, "--instance-id="+sentinel)

				stdout, stderr, code := runAdmin(t, home, args...)

				if code != 0 || stderr != "" {
					t.Errorf("delete exit = %d, stderr = %q; want 0 and empty", code, stderr)
				}
				var res struct {
					Results map[string]string `json:"results"`
				}
				want := map[string]string{id: "ok", sentinel: "ok"}
				if err := json.Unmarshal([]byte(stdout), &res); err != nil || !maps.Equal(res.Results, want) {
					t.Errorf("stdout = %q; want results %v (decode: %v)", stdout, want, err)
				}
				if _, err := os.Stat(decoy); !os.IsNotExist(err) {
					t.Errorf("decoy tree stat = %v; want absent", err)
				}
				assertRowGone(t, home, id)
				assertRowGone(t, home, sentinel)
			})
		}
	}
}

// b.t1b: a genuine -- and a stray operand end global scanning. Malformed
// global-looking operands must reach the verb's positional-argument refusal.
func TestAdminGlobalFlagsAfterParsingBoundary(t *testing.T) {
	cases := []struct {
		name string
		tail func(decoy string) []string
		want string
	}{
		{"separator store path", func(decoy string) []string {
			return []string{"--", "--store-path", filepath.Join(decoy, "state.db")}
		}, `unexpected argument "--store-path"`},
		{"separator home equals", func(decoy string) []string {
			return []string{"--", "--home=" + decoy}
		}, ""},
		{"operand before home", func(string) []string {
			return []string{"stray", "--home"}
		}, `unexpected argument "stray"`},
		{"operand before create if missing equals", func(string) []string {
			return []string{"stray", "--create-if-missing=not-a-bool"}
		}, `unexpected argument "stray"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedRow(t, store.StateEnded, "")
			before := rowColumns(t, home, id)
			decoy := filepath.Join(t.TempDir(), "decoy")
			args := append([]string{"delete", "--instance-id", id}, tc.tail(decoy)...)

			stdout, stderr, code := runAdmin(t, home, args...)

			// Check the filesystem even when the broken parser lets delete
			// succeed against a newly created decoy store.
			if _, err := os.Stat(decoy); !os.IsNotExist(err) {
				t.Errorf("decoy tree stat = %v; want absent", err)
			}
			assertRowUnchanged(t, home, id, before)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			want := tc.want
			if want == "" {
				want = `unexpected argument "--home=` + decoy + `"`
			}
			if env.ErrDescription != want {
				t.Errorf("err_description = %q; want %q", env.ErrDescription, want)
			}
		})
	}
}

// b.t1b: snapshot-store's own FlagSet must allow a real suffix global to
// select the source store, leaving the invoking HOME's poisoned store alone.
func TestAdminSnapshotSuffixGlobalSelectsSource(t *testing.T) {
	home := poisonedHome(t)
	beforeHome := dirEntries(t, home)
	sourceHome, id, _ := seedRow(t, store.StateEnded, "")
	beforeRow := rowColumns(t, sourceHome, id)
	directory := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create private snapshot directory: %v", err)
	}

	stdout, stderr, code := runAdmin(t, home, "snapshot-store", "--directory", directory,
		"--busy-timeout-ms", "1000", "--store-path", stateDB(sourceHome))

	if code != 0 || stderr != "" {
		t.Fatalf("snapshot exit = %d, stdout = %q, stderr = %q; want 0 and empty stderr", code, stdout, stderr)
	}
	var result struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Path != filepath.Join(directory, "state.db") {
		t.Fatalf("snapshot result = %q (%v); want path %q", stdout, err, filepath.Join(directory, "state.db"))
	}
	got, err := apitest.ReadSpawnColumns(result.Path, id)
	if err != nil || !reflect.DeepEqual(got, beforeRow) {
		t.Errorf("snapshot row = %+v (%v); want %+v", got, err, beforeRow)
	}
	assertRowUnchanged(t, sourceHome, id, beforeRow)
	if after := dirEntries(t, home); !maps.Equal(after, beforeHome) {
		t.Errorf("snapshot changed invoking HOME: %v; want %v", after, beforeHome)
	}
}
