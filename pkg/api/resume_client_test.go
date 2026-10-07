package api_test

// resume_client_test.go tests resume's starting-session rule through api.New
// and Client.Resume: the configured bound and window (AC-RES-05, AC-CFG-02),
// and a lookup reply with an unreadable creation time over test/fake-tmux
// (AC-RES-04). Its rows and starting cases are resume_starting_test.go's.

import (
	"bytes"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestResumeClientStartingSettings: the configured bound and window decide at
// value-1 s and value, each leaving the other at its default; 0 and absent keys give the defaults.
func TestResumeClientStartingSettings(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	cases := []struct {
		name          string
		settings      []apitest.TmuxSetting
		bound, window time.Duration
	}{
		{"bound at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)},
			secs(minB), defWindow},
		{"window at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)},
			defBound, secs(minW)},
		{"both set to 0", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, 0),
			apitest.TmuxInt(config.TmuxStoppingWindowSeconds, 0)}, defBound, defWindow},
		{"keys absent", nil, defBound, defWindow},
	}
	for _, tc := range cases {
		probes := []struct {
			name string
			row  startingRow
			want tmux.StartingSessionOutcome
		}{
			{"ended window-1 s ago, session the bound old",
				startingRow{state: store.StateEnded, endedAgo: tc.window - time.Second, age: tc.bound}, tmux.StillStopping},
			{"ended the window ago, session bound-1 s old",
				startingRow{state: store.StateEnded, endedAgo: tc.window, age: tc.bound - time.Second}, tmux.StillStarting},
			{"ended the window ago, session the bound old",
				startingRow{state: store.StateEnded, endedAgo: tc.window, age: tc.bound}, tmux.PastBoth},
		}
		for _, p := range probes {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedStarting(t, p.row)
				before := e.snapshotResume(t, r)
				_, logs, err := e.resumeClient(t, r.ID, tc.settings...)
				e.assertStartingRefusal(t, before, err, p.want, p.row.startingCase(r, tc.bound, tc.window))
				if logs != "" {
					t.Errorf("Client log = %q; want none (a refusal is reported by its error alone)", logs)
				}
			})
		}
	}
}

// TestResumeClientUnreadableCreationTime: over fake-tmux, a lookup reply whose own session has a non-decimal
// creation field is the lookup's unrecognised reply, never a starting-session answer; nothing is created or written.
func TestResumeClientUnreadableCreationTime(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv; it checks every record
	// written to the shared trail since its mark.
	e := newKillEnv(t)
	e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
	row := startingRow{state: store.StateEnded, endedAgo: defWindow + defBound, noSession: true}
	r := e.seedStarting(t, row)
	paneID := r.Spawn.Identity.PaneID
	// Without the injection the table's answer is the row's own young session: still starting.
	faketmuxfix.Tables{}.Write(t, r.Socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: killServerPID, Start: killServerStart},
		Sessions: []faketmuxfix.Session{{ID: "$0", Name: storedFormOf(r.Name), Created: e.ruleInstant().Unix(),
			Label: tmuxfix.LabelValue(r.Token, "$0", r.ID, r.StoreID),
			Panes: []faketmuxfix.Pane{{ID: paneID, PID: r.AgentPID, AdPane: tmuxfix.PaneLabelValue(r.Token, paneID)}}}},
		Injections: []faketmuxfix.Injection{
			{Call: tmux.CallLookup, Action: faketmuxfix.ActReply, Entry: "lookup/created-unparseable"}},
	})
	logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxCommand: faketmuxfix.Binary(t),
		Logger: log.New(&bytes.Buffer{}, "", 0)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, e.clock.Now)
	api.SetProcCheckerForTest(c, e.pc)

	before := e.snapshotResume(t, r)
	_, err = c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})

	assertOneName(t, err, "ErrTmuxUnresponsive")
	desc := apitest.DescUnrecognisedReply(tmux.CallLookup, "")
	young := row
	young.noSession = false
	for _, p := range apitest.DescStillStarting(young.startingCase(r, defBound, defWindow)).Require {
		if p != strconv.Quote(r.Name) {
			desc.MustNot = append(desc.MustNot, p)
		}
	}
	apitest.AssertDescription(t, err.Error(), desc, r.Token, r.StoreID)
	e.assertResumeWroteNothing(t, before)
	var calls [][]string
	for _, a := range fakeTmuxArgvs(t, logPath) {
		if slices.Contains(a, "new-session") {
			t.Errorf("fake-tmux ran a create %q; want none", a)
		}
		if slices.Contains(a, "-S") {
			calls = append(calls, a)
		}
	}
	if len(calls) != 1 || !isLookupOn(calls[0], r.Socket) {
		t.Errorf("fake-tmux socket calls = %q; want the one lookup on %s and no create", calls, r.Socket)
	}
}
