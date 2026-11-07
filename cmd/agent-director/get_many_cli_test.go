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
	words := []string{"A", "B,C", "-h", "--", "--x"}
	stdout, stderr, code := runCLIWithHome(t, home, append([]string{"get-many"}, words...)...)
	if code != 0 || stderr != "" || stdout != `{"spawns":[],"not_found":["A","B,C","-h","--","--x"]}`+"\n" {
		t.Fatalf("words: %s %s exit=%d", stdout, stderr, code)
	}
	selected := filepath.Join(t.TempDir(), "selected.db")
	if _, err := apitest.SeedSpawn(selected, "A", store.StateEnded, "/tmp", "off", "", true); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runCLIWithHome(t, home, "--home", home, "get-many", "A", "--store-path", selected, "B,C")
	var got api.GetManyResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || code != 0 || stderr != "" || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != "A" || !reflect.DeepEqual(got.NotFound, []string{"B,C"}) {
		t.Fatalf("selected store: %+v %s %s exit=%d err=%v", got, stdout, stderr, code, err)
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
	}{{nil, "ErrInvalidFlags", 0}, {[]string{ids[0], ""}, "ErrInvalidFlags", 0}, {[]string{ids[0], ids[0]}, "ErrInvalidFlags", 0}, {ids, "ErrTooManyIds", 20}} {
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
