//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorWorkingPauseRefusalAndQueuedExitAfterOwnedTermination(t *testing.T) {
	f, r, launch := newCursorPane(t, false)
	waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, nil)
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "HOLD"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "active peer receipt", func() bool {
		for _, frame := range r.Frames(t) {
			if string(frame["method"]) == `"session/prompt"` {
				return true
			}
		}
		return false
	}, nil)
	before := readRow(t, f.DBPath, launch.ID)
	path := before.JSONLPath.(string)
	for _, text := range []string{"queued-public-one", "queued-public-two"} {
		if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both follow-ups accepted", func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.Count(string(raw), `"kind":"submission_accepted"`) == 3
	}, nil)
	var runtimePID int
	if _, err := fmt.Sscan(readFileTrim(t, r.PID), &runtimePID); err != nil {
		t.Fatal(err)
	}
	if pidGone(runtimePID) {
		t.Fatal("runtime already gone before pause")
	}
	if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); !errors.Is(err, api.ErrSpawnNotPausable) {
		t.Fatalf("working pause guard: %v", err)
	}
	if pidGone(runtimePID) {
		t.Fatal("refused pause killed runtime")
	}
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/exit"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit published after runtime death", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateEnded && pidGone(runtimePID) }, nil)
	after := readRow(t, f.DBPath, launch.ID)
	if after.State != store.StateEnded || !pidGone(runtimePID) || after.SessionID != before.SessionID || after.LifeNumber != before.LifeNumber {
		t.Fatalf("pause lacks owned death or changed identity: %+v", after)
	}
	raw := rawInput(t, r)
	if strings.Contains(string(raw), "queued-public-") || strings.Contains(string(raw), `"text":"/exit"`) {
		t.Fatalf("pause submitted cancelled/model text: %s", raw)
	}
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]string{}
	cancelled := map[string]int{}
	ended := false
	for _, line := range strings.Split(strings.TrimSpace(string(transcript)), "\n") {
		var rec struct {
			Kind string `json:"kind"`
			Data struct {
				ID          string `json:"submission_id"`
				Text        string `json:"text"`
				RuntimeGone bool   `json:"runtime_gone"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		switch rec.Kind {
		case "submission_accepted":
			accepted[rec.Data.ID] = rec.Data.Text
		case "submission_cancelled":
			cancelled[rec.Data.ID]++
		case "session_end":
			ended = rec.Data.RuntimeGone
		}
	}
	for id, text := range accepted {
		if strings.HasPrefix(text, "queued-public-") && cancelled[id] != 1 {
			t.Fatalf("queue cancellation missing or duplicated: %s %v", id, cancelled)
		}
	}
	if !ended {
		t.Fatal("session end lacks runtime death evidence")
	}
}
