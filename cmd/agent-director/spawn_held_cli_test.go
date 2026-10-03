package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// A plain spawn whose create answers "duplicate session" through the built CLI
// (SR-9.4, SR-3.10, SR-14): the new row is ended, the holder is classified by
// its label and left alone, and one ad.launch.name_held is written. Each test
// has its own HOME and TMUX_TMPDIR; test/fake-tmux answers "duplicate session"
// for a name its table holds.

// heldName is the requested session name every held-name case spawns under.
const heldName = "w13-held"

// heldCreated is the holder sessions' #{session_created}.
const heldCreated = 1790549182

// heldHome bootstraps home's store and returns the launch socket and this
// store's id.
func heldHome(t *testing.T, home string) (socket, storeID string) {
	t.Helper()
	bootstrapDB(t, home)
	storeID, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return spawnSocket(t, home), storeID
}

// writeHolders makes sessions the fake's table on socket, under a running
// server.
func writeHolders(t *testing.T, socket string, sessions ...faketmuxfix.Session) {
	t.Helper()
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{
		Server:   &faketmuxfix.Server{PID: os.Getpid(), Start: heldCreated},
		Sessions: sessions,
	})
}

// holderSession is a session named heldName with id sessionID and label
// ("" = unset), whose one pane is this test process.
func holderSession(sessionID, label string) faketmuxfix.Session {
	return faketmuxfix.Session{
		ID: sessionID, Created: heldCreated, Name: heldName, Label: label,
		Panes: []faketmuxfix.Pane{{ID: "%" + strings.TrimPrefix(sessionID, "$"), PID: os.Getpid()}},
	}
}

// spawnHeld runs a minted-id plain spawn of heldName under home, requires exit
// 1 with no ErrTmuxSessionNameTaken anywhere, and returns the envelope.
func spawnHeld(t *testing.T, home, fakeDir string, extraEnv map[string]string) errorEnvelope {
	t.Helper()
	_, stderr, code := runSpawnCLIEnv(t, home, fakeDir, extraEnv,
		"spawn", "--cwd", t.TempDir(), "--tmux-session-name", heldName, "--no-pre-trust")
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr=%q)", code, stderr)
	}
	if strings.Contains(stderr, "ErrTmuxSessionNameTaken") {
		t.Errorf("held name surfaced ErrTmuxSessionNameTaken: %q", stderr)
	}
	return parseEnvelope(t, lastJSONLine(stderr))
}

// heldRowID returns the id of the one row under home: the held spawn's.
func heldRowID(t *testing.T, home, fakeDir string) string {
	t.Helper()
	ids := spawnRowIDs(t, home, fakeDir)
	if len(ids) != 1 {
		t.Fatalf("rows = %q; want the one row the spawn inserted", ids)
	}
	return ids[0]
}

// assertHeldRowEnded fails unless id's row was ended by the end write: status
// ended, ended_at set, no launch start, row_version 1 (Appendix F.4).
func assertHeldRowEnded(t *testing.T, home, fakeDir, id string) {
	t.Helper()
	if st := statusOf(t, home, fakeDir, id); st != string(store.StateEnded) {
		t.Errorf("status = %q; want %s", st, store.StateEnded)
	}
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.EndedAt == nil || cols.LaunchStartedAt != nil || fmt.Sprint(cols.RowVersion) != "1" {
		t.Errorf("ended_at = %v, launch_started_at = %v, row_version = %v; want set, NULL, 1",
			cols.EndedAt, cols.LaunchStartedAt, cols.RowVersion)
	}
}

// nameHeldRecords returns home's ad.launch.name_held records for id.
func nameHeldRecords(t *testing.T, home, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range trailLinesOrNil(t, trailDir(home)) {
		if l["event"] == "ad.launch.name_held" && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// TestSpawnCLIHeldNameClassified: the holder of a held name is classified by
// its label; the row is ended, the holder untouched, one name_held written.
func TestSpawnCLIHeldNameClassified(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	otherID := "id-w13-other-" + uuid.NewString()[:8]
	cases := []struct {
		name     string
		holder   func(storeID string) []faketmuxfix.Session
		env      map[string]string
		wantErr  string
		holderID string                           // the holder's $N; "" when none is listed
		desc     func(id string) apitest.DescCase // id: the new row's
	}{
		{
			name:     "no valid label",
			holder:   func(string) []faketmuxfix.Session { return []faketmuxfix.Session{holderSession("$7", "")} },
			wantErr:  "ErrTmuxSessionConflict",
			holderID: "$7",
			desc: func(string) apitest.DescCase {
				return apitest.DescHeldNoValidID(apitest.HeldName{Name: heldName, SessionID: "$7", Row: apitest.HeldRowEnded})
			},
		},
		{
			name: "another row's label",
			holder: func(storeID string) []faketmuxfix.Session {
				return []faketmuxfix.Session{holderSession("$8", tmuxfix.LabelValue(tmuxfix.OtherToken, "$8", otherID, storeID))}
			},
			wantErr:  "ErrTmuxSessionConflict",
			holderID: "$8",
			desc: func(string) apitest.DescCase {
				return apitest.DescHeldDifferentID(apitest.HeldName{Name: heldName, SessionID: "$8", Row: apitest.HeldRowEnded})
			},
		},
		{
			name:    "vanished",
			holder:  func(string) []faketmuxfix.Session { return nil },
			env:     map[string]string{faketmuxfix.EnvFailNewSessionName: heldName},
			wantErr: "ErrTmuxSessionCreate",
			desc: func(id string) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: heldName, Duplicate: true}).
					AfterHeldName(apitest.HeldName{Name: heldName, Row: apitest.HeldRowEnded, InstanceID: id})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			socket, storeID := heldHome(t, home)
			writeHolders(t, socket, tc.holder(storeID)...)
			before := faketmuxfix.Tables{}.Read(t, socket).Sessions

			env := spawnHeld(t, home, fakeDir, tc.env)
			id := heldRowID(t, home, fakeDir)
			if env.ErrName != tc.wantErr {
				t.Errorf("err_name = %q; want %q (desc=%q)", env.ErrName, tc.wantErr, env.ErrDescription)
			}
			token, _, _ := launchIdentity(t, home, id)
			apitest.AssertDescription(t, env.ErrDescription, tc.desc(id), token, storeID, otherID, tmuxfix.OtherToken)
			assertHeldRowEnded(t, home, fakeDir, id)
			assertInvocationKinds(t, home, "new-session", "list-sessions")
			if after := (faketmuxfix.Tables{}).Read(t, socket).Sessions; !reflect.DeepEqual(after, before) {
				t.Errorf("fake sessions changed:\nbefore=%+v\nafter =%+v", before, after)
			}

			recs := nameHeldRecords(t, home, id)
			if len(recs) != 1 {
				t.Fatalf("ad.launch.name_held records for %s = %d; want 1: %v", id, len(recs), recs)
			}
			rec := recs[0]
			want := map[string]any{
				"source": "ad_spawn", "launch": "spawn", "row_result": "ended", "outcome": tc.wantErr,
				"tmux_session_name": heldName, "tmux_socket": socket, "store_id": storeID,
			}
			if tc.holderID == "" {
				for _, k := range []string{"tmux_session_id", "carries_this_id", "attach_command", "end_command"} {
					want[k] = nil
				}
			} else {
				want["tmux_session_id"], want["carries_this_id"] = tc.holderID, false
				for _, k := range []string{"attach_command", "end_command"} {
					cmd, _ := rec[k].(string)
					if !strings.Contains(cmd, "-S") || !strings.Contains(cmd, tc.holderID) {
						t.Errorf("%s = %v; want a command with -S and %s", k, rec[k], tc.holderID)
					}
				}
			}
			for k, v := range want {
				if got, ok := rec[k]; !ok || got != v {
					t.Errorf("name_held %s = %v (present=%v); want %v", k, got, ok, v)
				}
			}
			for k, v := range rec {
				if s, _ := v.(string); strings.Contains(s, otherID) {
					t.Errorf("name_held %s = %q names another row's id", k, s)
				}
			}
		})
	}
}

// TestSpawnCLIHeldNameEndedSticks: after the held spawn ends its row, the
// leftover's hooks leave it ended, each writing one no_pane_recorded ignore.
func TestSpawnCLIHeldNameEndedSticks(t *testing.T) {
	h := gateHome{home: t.TempDir(), fakeDir: buildFakeTmux(t)}
	socket, _ := heldHome(t, h.home)
	writeHolders(t, socket, holderSession("$7", ""))
	env := spawnHeld(t, h.home, h.fakeDir, nil)
	id := heldRowID(t, h.home, h.fakeDir)
	if env.ErrName != "ErrTmuxSessionConflict" {
		t.Fatalf("err_name = %q; want ErrTmuxSessionConflict (desc=%q)", env.ErrName, env.ErrDescription)
	}
	if st := h.row(t, id).State; st != store.StateEnded {
		t.Fatalf("row state after held spawn = %q; want %s", st, store.StateEnded)
	}
	// The hook path makes no tmux call; start its log empty.
	if err := os.Remove(filepath.Join(h.home, "fake-tmux.log")); err != nil {
		t.Fatalf("clear fake-tmux log: %v", err)
	}

	transcript := h.transcript(t, "held-leftover-uuid")
	hooks := []struct{ event, payload string }{
		{"Stop", `{"hook_event_name":"Stop"}`},
		{"SessionStart", `{"hook_event_name":"SessionStart","source":"startup","transcript_path":"` + transcript + `"}`},
	}
	for i, hk := range hooks {
		if out := h.hook(t, id, "", hk.payload); out != "" {
			t.Errorf("%s stdout = %q; want empty", hk.event, out)
		}
		sp := h.row(t, id)
		if sp.State != store.StateEnded || sp.ClaudeSessionID != "" {
			t.Errorf("after %s: state = %q, session = %q; want %s and none", hk.event, sp.State, sp.ClaudeSessionID, store.StateEnded)
		}
		ign := trailEvents(t, h.home, "ad.hook.ignored")
		if len(ign) != i+1 {
			t.Fatalf("after %s: ad.hook.ignored lines = %d; want %d: %v", hk.event, len(ign), i+1, ign)
		}
		got := ign[i]
		if got["claude_instance_id"] != id || got["hook_event"] != hk.event || got["reason"] != store.HookReasonNoPaneRecorded {
			t.Errorf("ad.hook.ignored = %v; want %s, %s, %s", got, id, hk.event, store.HookReasonNoPaneRecorded)
		}
	}
}

// TestSpawnCLIHeldNameTrailFailOpen: with the trail file unwritable, the held
// spawn's exit code, envelope and ended row are unchanged (SR-14 fail-open).
func TestSpawnCLIHeldNameTrailFailOpen(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	socket, _ := heldHome(t, home)
	writeHolders(t, socket, holderSession("$7", ""))
	// A read-only ~/.agent-director would also stop the store opening, so the
	// trail file itself is made read-only (empty, so no earlier record counts).
	trailFile := filepath.Join(trailDir(home), "ad-trail.jsonl")
	if err := os.WriteFile(trailFile, nil, 0o400); err != nil {
		t.Fatalf("create read-only trail file: %v", err)
	}
	if err := os.Chmod(trailFile, 0o400); err != nil {
		t.Fatalf("chmod trail file: %v", err)
	}

	env := spawnHeld(t, home, fakeDir, nil)
	if env.ErrName != "ErrTmuxSessionConflict" {
		t.Fatalf("err_name = %q; want ErrTmuxSessionConflict (desc=%q)", env.ErrName, env.ErrDescription)
	}
	id := heldRowID(t, home, fakeDir)
	apitest.AssertDescription(t, env.ErrDescription,
		apitest.DescHeldNoValidID(apitest.HeldName{Name: heldName, SessionID: "$7", Row: apitest.HeldRowEnded}))
	assertHeldRowEnded(t, home, fakeDir, id)
	if fi, err := os.Stat(trailFile); err != nil || fi.Size() != 0 {
		t.Errorf("trail file after spawn: %v (err %v); want it still empty (the write must have failed)", fi, err)
	}
}
