//go:build linux

package api_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorSavedSelectionRefusal(t *testing.T) {
	// Serial: protected-history fixture sets HOME and checks the trail's no-write contract.
	for _, selection := range []map[string]string{{"CURSOR_API_KEY": "synthetic-saved-private"}, {"CURSOR_API_KEY": ""}, {"AGENT_CLI_CREDENTIAL_STORE": "memory"}, {"CURSOR_DATA_DIR": "relative"}} {
		t.Run("saved selection", func(t *testing.T) {
			e := newResumeEnv(t)
			id, _ := seedCursorResume(t, e, store.StateEnded, selection)
			before := e.columns(t, id)
			mark := trailMark(t)
			history, historyErr := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
			if historyErr != nil {
				t.Fatal(historyErr)
			}
			_, err := e.resume(id)
			assertOneName(t, err, "ErrReservedEnvKey")
			if strings.Contains(errText(err), "synthetic-saved-private") {
				t.Fatal("saved secret leaked")
			}
			afterHistory, historyErr := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
			if historyErr != nil || !reflect.DeepEqual(before, e.columns(t, id)) || !reflect.DeepEqual(history, afterHistory) {
				t.Fatal("refused saved profile changed row/history")
			}
			assertNoTmuxCalls(t, e.rec)
			assertNoTrailSince(t, mark, id)
		})
	}
}

func TestCursorResumeKeepsSavedDirectories(t *testing.T) {
	// Serial: protected-history fixture and conflicting ambient selection set process environment.
	e := newResumeEnv(t)
	id, _ := seedCursorResume(t, e, store.StateEnded, map[string]string{"CURSOR_CONFIG_DIR": "/saved/config", "CURSOR_DATA_DIR": "/saved/data"})
	t.Setenv("CURSOR_CONFIG_DIR", "/ambient/config")
	t.Setenv("CURSOR_DATA_DIR", "/ambient/data")
	before := e.columns(t, id)
	if _, err := e.resume(id); err != nil {
		t.Fatal(err)
	}
	calls := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(calls) != 1 || calls[0].Envs["CURSOR_CONFIG_DIR"] != "/saved/config" || calls[0].Envs["CURSOR_DATA_DIR"] != "/saved/data" {
		t.Fatalf("saved selection changed: %+v", calls)
	}
	if after := e.columns(t, id); after.ExtraEnv != before.ExtraEnv || after.LifeNumber != before.LifeNumber {
		t.Fatal("resume changed selection/life")
	}
}
