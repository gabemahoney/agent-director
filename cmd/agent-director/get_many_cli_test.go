package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCLIGetManyWordsAndGlobalFlags(t *testing.T) {
	home := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected.db")
	if _, err := apitest.SeedSpawn(selected, "A", store.StateEnded, "/tmp", "off", "", true); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	// b.t1b: globals select the store only before get-many. Every later
	// word is an id, including global names, their values and separators.
	for _, tc := range []struct {
		name  string
		words []string
	}{
		{"control words", []string{"B,C", "-h", "--", "--x"}},
		{"global words are ids", []string{
			"--store-path", filepath.Join(decoy, "s.db"),
			"--home", filepath.Join(decoy, "home"),
			"--tmux-command", filepath.Join(decoy, "tmux"),
			"--create-if-missing", "false",
			"--store-path=" + filepath.Join(decoy, "equals.db"),
			"--home=" + filepath.Join(decoy, "equals-home"),
			"--tmux-command=" + filepath.Join(decoy, "equals-tmux"),
			"--create-if-missing=true",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--home", home, "--store-path", selected, "get-many", "A"}, tc.words...)
			stdout, stderr, code := runCLIWithHome(t, home, args...)
			assertHomeTree(t, decoy)
			var got api.GetManyResult
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || code != 0 || stderr != "" || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != "A" || !reflect.DeepEqual(got.NotFound, tc.words) {
				t.Fatalf("selected store: %+v stdout=%q stderr=%q exit=%d err=%v; want row A and not_found=%q", got, stdout, stderr, code, err, tc.words)
			}
		})
	}
}

func TestCLIGetManyRefusalsAndCapReload(t *testing.T) {
	home := t.TempDir()
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = fmt.Sprintf("many-%02d", i)
	}
	for _, id := range ids[:3] {
		if _, err := apitest.SeedSpawn(stateDB(home), id, store.StateEnded, "/tmp", "off", "", true); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		ids  []string
		name string
		cap  int
	}{{nil, "ErrInvalidFlags", 0}, {[]string{ids[0], ""}, "ErrInvalidFlags", 0}, {[]string{ids[0], ids[0]}, "ErrInvalidFlags", 0}, {ids, "ErrTooManyIds", 20}, {append(ids[:20:20], ids[0]), "ErrTooManyIds", 20}} {
		out, errout, code := runCLIWithHome(t, home, append([]string{"get-many"}, tc.ids...)...)
		assertOnlyEnvelope(t, out, errout, code, tc.name)
		if tc.cap != 0 {
			assertGetManyCLICap(t, errout, tc.cap)
		}
	}
	for _, cap := range []int{2, 1} {
		if err := os.WriteFile(filepath.Join(directorDir(home), "config.toml"), []byte(fmt.Sprintf("[get_many]\nid_cap = %d\n", cap)), 0600); err != nil {
			t.Fatal(err)
		}
		out, errout, code := runCLIWithHome(t, home, append([]string{"get-many"}, ids[:cap+1]...)...)
		assertOnlyEnvelope(t, out, errout, code, "ErrTooManyIds")
		assertGetManyCLICap(t, errout, cap)
		out, errout, code = runCLIWithHome(t, home, append([]string{"get-many"}, ids[:cap]...)...)
		var got api.GetManyResult
		if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 || errout != "" || len(got.Spawns) != cap || got.NotFound == nil || len(got.NotFound) != 0 {
			t.Fatalf("cap %d passing: %+v %s exit=%d err=%v", cap, got, errout, code, err)
		}
	}
}

func assertGetManyCLICap(t *testing.T, raw string, cap int) {
	t.Helper()
	var envelope struct {
		Details map[string]int `json:"err_details"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil || !reflect.DeepEqual(envelope.Details, map[string]int{"id_cap": cap}) {
		t.Fatalf("cap details: %s err=%v", raw, err)
	}
}
