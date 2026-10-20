package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

// The CLI sends one exit command and preserves the inspection error envelope.
func TestCLIPauseTimeoutInspectionRequired(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, socket := seedRowOnSocket(t, store.StateWaiting)
	faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "Background work is running\n1. Exit and stop tasks / 2. Stay\n")))
	configPath := filepath.Join(directorDir(home), "config.toml")
	if err := os.WriteFile(configPath, []byte("[pause]\ntimeout_seconds = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "pause", "--instance-id", id)
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrPauseTimeout")
	var envelope struct {
		Details map[string]any `json:"err_details"`
	}
	want := map[string]any{"inspection_required": true, "exit_keys_sent": true, "exit_confirmed": false, "row_state": "waiting"}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil || !reflect.DeepEqual(envelope.Details, want) {
		t.Fatalf("pause envelope = %s (%v); want err_details %v", stderr, err, want)
	}
	assertInvocationKinds(t, home, "list-sessions", "list-panes", "send-keys", "send-keys", "send-keys")
}
