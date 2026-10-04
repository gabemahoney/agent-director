package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Lead screens: Claude Code's idle prompt, and a selection dialog.
const (
	promptScreen = "────────\n❯ \n────────\n  ? for shortcuts\n"
	dialogScreen = " Trust this folder?\n ❯ 1. Yes\n   2. No\n Enter to confirm · Esc to cancel\n"
)

func TestInputReady(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		ready      bool
		why        string
	}{
		{"prompt line and hint", promptScreen, true, `prompt line and "? for shortcuts" seen`},
		{"an older > prompt in a box", "│ > \n", true, "prompt line seen"},
		{"the hint only", "  ? for shortcuts\n", true, `"? for shortcuts" seen`},
		{"a numbered selection", " ❯ 1. Dark mode\n   2. Light mode\n", false, "a selection dialog is showing"},
		{"a dialog footer under a prompt line", "❯ \n Esc to exit\n", false, `a dialog is showing ("Esc to exit")`},
		{"press enter", "Welcome\nPress Enter to continue\n", false, `a dialog is showing ("Press Enter to continue")`},
		{"a marker without a space", "❯foo\n", false, "no prompt line"},
		{"a blank screen", "", false, "no prompt line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ready, why := inputReady(tc.text); ready != tc.ready || why != tc.why {
				t.Errorf("inputReady = %t, %q; want %t, %q", ready, why, tc.ready, tc.why)
			}
		})
	}
}

func TestPaneExcerpt(t *testing.T) {
	long := strings.Repeat("é", paneExcerptMax+50)
	for _, tc := range []struct{ name, text, want string }{
		{"the last six non-empty lines", "1\n2\n\n3\n4\n5\n6\n  7  \n8\n", "3 | 4 | 5 | 6 | 7 | 8"},
		{"blank", " \n\n", "(blank)"},
		{"bounded in runes", long, "…" + strings.Repeat("é", paneExcerptMax)},
	} {
		if got := paneExcerpt(tc.text); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// screenRig is a rig whose lead pane %1 shows screen(elapsed) on capture,
// scrubbing the gateway sentinels.
func screenRig(t *testing.T, screen func(elapsed time.Duration) (string, int)) (*rig, *rn9Agent) {
	t.Helper()
	r := newRig(t, modeDry)
	r.h.scr = gatewayScrubber()
	r.h.inv.scr = r.h.scr
	r.h.cfg.inputReadyTimeout = 10 * time.Second
	r.ex.reply = func(argv []string) (string, string, int) {
		if slices.Contains(argv, "capture-pane") {
			out, code := screen(r.clk.Now().Sub(clockStart))
			return out, "capture failed", code
		}
		return `{"claude_instance_id":"id-1"}`, "", 0
	}
	sock := privateSocket(r.h.iso.TmuxTmpdir)
	return r, &rn9Agent{h: r.h, caseID: rn9TeamSplitPaneID, a: agentRef{InstanceID: "id-1", Socket: sock, PaneID: "%1"}, answered: map[string]bool{}}
}

func TestWaitInputReady(t *testing.T) {
	t.Run("ready, then the grace", func(t *testing.T) {
		r, d := screenRig(t, func(time.Duration) (string, int) { return promptScreen, 0 })
		if err := d.waitInputReady(); err != nil {
			t.Fatal(err)
		}
		want := []string{"tmux", "-S", d.a.Socket, "capture-pane", "-p", "-J", "-t", "%1"}
		if !reflect.DeepEqual(r.ex.calls[0], want) || r.clk.Now().Sub(clockStart) != inputReadyGrace {
			t.Errorf("first call %q, elapsed %s", r.ex.calls[0], r.clk.Now().Sub(clockStart))
		}
		if !reflect.DeepEqual(d.notes, []string{`input ready after 0s (prompt line and "? for shortcuts" seen); team prompt sent 2s later`}) {
			t.Errorf("notes %q", d.notes)
		}
	})
	t.Run("ready late", func(t *testing.T) {
		r, d := screenRig(t, func(e time.Duration) (string, int) {
			if e < 3*time.Second {
				return "Loading…\n", 0
			}
			return promptScreen, 0
		})
		if err := d.waitInputReady(); err != nil {
			t.Fatal(err)
		}
		if r.clk.Now().Sub(clockStart) != 3*time.Second+inputReadyGrace || !strings.HasPrefix(d.notes[0], "input ready after 3s (") {
			t.Errorf("elapsed %s, notes %q", r.clk.Now().Sub(clockStart), d.notes)
		}
	})
	// The check reads text with only the exact values replaced: a prompt
	// line naming a credential is still seen.
	t.Run("a prompt line naming a credential", func(t *testing.T) {
		_, d := screenRig(t, func(time.Duration) (string, int) { return "❯ paste your auth token " + scrubToken[2:14] + "\n", 0 })
		if err := d.waitInputReady(); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name   string
		screen string
		code   int
		want   []string
	}{
		{"a dialog never ready", dialogScreen, 0, []string{"the lead's prompt box was not seen within 10s (a selection dialog is showing); the team prompt was not sent; " +
			"pane shows: Trust this folder? | ❯ 1. Yes | 2. No | Enter to confirm · Esc to cancel"}},
		{"the excerpt is scrubbed as evidence", "Authorization: Bearer x\nreach Gateway.Example.invalid " + scrubToken[2:14] + "\nLoading…\n", 0,
			[]string{"pane shows: " + withheldLine + " | reach <redacted> <redacted> | Loading…"}},
		{"every capture failed", "", 1, []string{"not seen within 10s; last capture failed: ", "pane shows: (blank)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := screenRig(t, func(time.Duration) (string, int) { return tc.screen, tc.code })
			err := d.waitInputReady()
			if !errors.Is(err, errInputNeverReady) || r.clk.Now().Sub(clockStart) != 10*time.Second {
				t.Fatalf("err %v after %s", err, r.clk.Now().Sub(clockStart))
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
			assertAbsent(t, "error", strings.ToLower(err.Error()), "gateway.example.invalid", scrubToken[2:14], "bearer")
		})
	}
}

func TestCapturePanes(t *testing.T) {
	r := newRig(t, modeDry)
	r.h.scr = gatewayScrubber()
	sock := privateSocket(r.h.iso.TmuxTmpdir)
	swarm := filepath.Join(filepath.Dir(sock), "claude-swarm-77")
	writeFile(t, swarm, "")
	r.ex.reply = func(argv []string) (string, string, int) {
		on, last := argv[2], argv[len(argv)-1]
		switch {
		case argv[3] == "list-panes" && on == sock:
			return "%1\n%2\n%3\n", "", 0
		case argv[3] == "list-panes":
			return "%0\n", "", 0
		case last == "%3":
			return "", "can't find pane", 1
		}
		return fmt.Sprintf("screen of %s on %s\nAuthorization: Bearer x\nhost gateway.example.invalid\n", last, filepath.Base(on)), "", 0
	}
	a := agentRef{Socket: sock, PaneID: "%1", TmuxSessionID: "$1"}
	got, notes := r.h.capturePanes("rn9.team-splitpane", a, "-after-pause")
	var files []string
	for _, c := range got {
		files = append(files, c.File)
	}
	want := []string{"rn9.team-splitpane-pane-lead-after-pause.txt", "rn9.team-splitpane-pane-teammate-p2-after-pause.txt",
		"rn9.team-splitpane-pane-claude-swarm-77-p0-after-pause.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("captures %q, want %q", files, want)
	}
	if !reflect.DeepEqual(notes, []string{"pane %3 (teammate-p3): capture failed: tmux -S " + sock + " capture-pane: exit 1: can't find pane"}) {
		t.Errorf("notes %q", notes)
	}
	text := readFile(t, filepath.Join(r.h.cfg.outDir, want[2]))
	if text != "screen of %0 on claude-swarm-77\n"+withheldLine+"\nhost <redacted>\n" || got[2].Text != text {
		t.Errorf("swarm capture %q", text)
	}
	if r.ex.count("capture-pane", "-S", "-", "-t", "%1") != 1 || r.ex.count("list-panes", "-s", "-t", "$1") != 1 {
		t.Errorf("calls %q", r.ex.calls)
	}
}

// TestCapturePanesNotes: a failed listing is a note, and the lead's own pane
// is still captured.
func TestCapturePanesNotes(t *testing.T) {
	r := newRig(t, modeDry)
	r.ex.reply = func(argv []string) (string, string, int) {
		if argv[3] == "list-panes" {
			return "", "no server running", 1
		}
		return "lead screen\n", "", 0
	}
	got, notes := r.h.capturePanes("rn9.team-inprocess", agentRef{Socket: privateSocket(r.h.iso.TmuxTmpdir), PaneID: "%1", TmuxSessionID: "$1"}, "")
	if len(got) != 1 || got[0].File != "rn9.team-inprocess-pane-lead.txt" || len(notes) != 1 || !strings.HasPrefix(notes[0], "panes of the lead's session not listed: ") {
		t.Errorf("captures %+v, notes %q", got, notes)
	}
}

func TestKeepDebugLogs(t *testing.T) {
	r := newRig(t, modeDry)
	r.h.scr = gatewayScrubber()
	dbg := r.h.claudeDebugDir()
	writeFile(t, filepath.Join(dbg, "old.txt"), "old\n")
	writeFile(t, filepath.Join(dbg, "changed.txt"), "before\n")
	before := r.h.debugLogStamps()
	writeFile(t, filepath.Join(dbg, "changed.txt"), "before\nafter\n")
	src := "start\napi_key=abc\nhost Gateway.Example.invalid:8443 " + scrubToken + "\n"
	writeFile(t, filepath.Join(dbg, "new.txt"), src)
	lead, err := r.h.leadDebugFile("rn9.team-splitpane")
	if err != nil || lead != filepath.Join(r.h.iso.Home, "mx-claude-debug", "rn9.team-splitpane-lead.txt") {
		t.Fatalf("lead debug file %q, %v", lead, err)
	}
	writeFile(t, lead, src)
	files, notes := r.h.keepDebugLogs("rn9.team-splitpane", lead, before)
	want := []string{"rn9.team-splitpane-claude-debug-lead.txt", "rn9.team-splitpane-claude-debug-changed.txt", "rn9.team-splitpane-claude-debug-new.txt"}
	if !reflect.DeepEqual(files, want) || len(notes) != 0 {
		t.Fatalf("files %q, notes %q; want %q", files, notes, want)
	}
	for _, f := range []string{want[0], want[2]} {
		if got := readFile(t, filepath.Join(r.h.cfg.outDir, f)); got != "start\n"+withheldLine+"\nhost <redacted> <redacted>\n" {
			t.Errorf("%s: %q", f, got)
		}
	}
	if _, notes := r.h.keepDebugLogs("rn9.team-inprocess", filepath.Join(t.TempDir(), "none.txt"), r.h.debugLogStamps()); !reflect.DeepEqual(notes,
		[]string{"Claude debug log: the lead wrote none to its --debug-file"}) {
		t.Errorf("a missing lead log: notes %q", notes)
	}
}

// TestCopyScrubbedKeepsTheTail: a log over the bound keeps its tail from the
// first whole line on.
func TestCopyScrubbedKeepsTheTail(t *testing.T) {
	const line = "0123456789 abcdef\n"
	body := strings.Repeat(line, maxDebugCopyBytes/len(line)+10)
	src := filepath.Join(t.TempDir(), "big.txt")
	writeFile(t, src, "HEAD auth line\n"+body)
	dst := filepath.Join(t.TempDir(), "copy.txt")
	dropped, err := copyScrubbed(src, dst, scrubber{})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, dst)
	total := int64(len("HEAD auth line\n") + len(body))
	if dropped+int64(len(got)) != total || int64(len(got)) > maxDebugCopyBytes {
		t.Errorf("dropped %d + kept %d, want %d in all, at most %d kept", dropped, len(got), total, maxDebugCopyBytes)
	}
	if !strings.HasSuffix(body, got) || len(got)%len(line) != 0 || strings.Contains(got, "HEAD") {
		t.Errorf("the copy does not start at a whole line (%d bytes)", len(got))
	}
}

// teamWorld answers an agent team lead's calls on top of world: its row's
// session id, its screen, the team prompt and pause.
type teamWorld struct {
	*world
	t      *testing.T
	file   string // the scenario's recorder file
	screen string // the lead's visible screen
	accept bool   // the lead takes the team prompt (UserPromptSubmit, Stop)
	pause  int    // pause's exit code
}

// record appends the lead's own hook lines to the recorder file.
func (w *teamWorld) record(events ...string) {
	f, err := os.OpenFile(w.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()
	for _, e := range events {
		fmt.Fprintf(f, `{"event":%q,"source":"startup","transcript_basename":"sess-lead","parent_pid":4242,"instance_id":"id-1","keys":[]}`+"\n", e)
	}
}

func (w *teamWorld) reply(argv []string) (string, string, int) {
	switch {
	case argv[0] == "agent-director" && argv[1] == "get":
		return fmt.Sprintf(`{"claude_instance_id":"id-1","state":"waiting","tmux_socket":%q,"claude_session_id":"sess-lead"}`, w.socket), "", 0
	case argv[0] == "agent-director" && argv[1] == "send-keys":
		if w.accept {
			w.record("UserPromptSubmit", "Stop")
		}
		return "", "", 0
	case argv[0] == "agent-director" && argv[1] == "pause":
		return "", "did not reach ended within 30s", w.pause
	case slices.Contains(argv, "capture-pane") && slices.Contains(argv, "-"):
		return "history of " + argv[len(argv)-1] + "\n" + w.screen, "", 0
	case slices.Contains(argv, "capture-pane"):
		return w.screen, "", 0
	case slices.Contains(argv, "list-panes") && argv[len(argv)-1] == "#{pane_id}":
		return "%1\n", "", 0
	}
	return w.world.reply(argv)
}

// callIndex is the index of the first call after from containing every word.
func callIndex(calls [][]string, from int, words ...string) int {
	for i := from; i < len(calls); i++ {
		joined := " " + strings.Join(calls[i], " ") + " "
		all := true
		for _, w := range words {
			all = all && strings.Contains(joined, " "+w+" ")
		}
		if all {
			return i
		}
	}
	return -1
}

// TestRunTeamCutShort drives rn9.team-splitpane against teamWorld: a lead
// whose input never shows or who ignores the team prompt gets no further
// key, and its panes are captured before pause and again after a failed one.
func TestRunTeamCutShort(t *testing.T) {
	const id = rn9TeamSplitPaneID
	for _, tc := range []struct {
		name          string
		screen        string
		accept        bool
		pause         int
		sends         int
		reasonPrefix  string
		paneArtifacts []string
	}{
		{"input never ready", dialogScreen, false, 0, 0, "team run cut short: input never ready: the lead's prompt box was not seen within 10s",
			[]string{id + "-pane-lead.txt"}},
		{"input never ready and pause fails", dialogScreen, false, 1, 0, "team run cut short: input never ready: ",
			[]string{id + "-pane-lead.txt", id + "-pane-lead-after-pause.txt"}},
		{"the prompt not accepted", promptScreen, false, 0, 1, "team run cut short: the team prompt was not accepted: no UserPromptSubmit within 10s",
			[]string{id + "-pane-lead.txt"}},
		{"the team settles", promptScreen, true, 0, 1, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, modeDry)
			r.h.cfg.inputReadyTimeout, r.h.cfg.promptAcceptWait, r.h.cfg.settleQuiet, r.h.cfg.stepTimeout = 10*time.Second, 10*time.Second, time.Second, 30*time.Second
			w := &teamWorld{world: newWorld(r), t: t, file: r.h.recorderFile(id), screen: tc.screen, accept: tc.accept, pause: tc.pause}
			r.ex.reply = w.reply
			w.record("SessionStart")
			lead, err := r.h.leadDebugFile(id)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, lead, "lead debug\n")
			if _, err := caseByID(t, id).run(r.h); err != nil {
				t.Fatal(err)
			}
			sc := r.h.res.RN9.Scenarios[0]
			if n := r.ex.count("agent-director", "send-keys"); n != tc.sends {
				t.Errorf("%d send-keys, want %d", n, tc.sends)
			}
			if !strings.Contains(strings.Join(r.ex.calls[0], " "), " -- --debug-file "+lead) {
				t.Errorf("spawn %q does not pass the lead's --debug-file", r.ex.calls[0])
			}
			wantArtifacts := append(append([]string(nil), tc.paneArtifacts...), id+"-claude-debug-lead.txt")
			if !reflect.DeepEqual(sc.Artifacts, wantArtifacts) {
				t.Errorf("artifacts %q, want %q", sc.Artifacts, wantArtifacts)
			}
			pause := callIndex(r.ex.calls, 0, "pause")
			before, after := callIndex(r.ex.calls, 0, "capture-pane", "-"), callIndex(r.ex.calls, pause, "capture-pane", "-")
			if wantBefore := len(tc.paneArtifacts) > 0; (before >= 0 && before < pause) != wantBefore {
				t.Errorf("history capture at call %d, pause at %d; want one before pause: %t", before, pause, wantBefore)
			}
			if wantAfter := len(tc.paneArtifacts) > 1; (after >= 0) != wantAfter {
				t.Errorf("history capture after pause at call %d, want one: %t", after, wantAfter)
			}
			if tc.reasonPrefix == "" {
				if sc.Reason != "no hook from a split-pane teammate's process was recorded" {
					t.Errorf("reason %q", sc.Reason)
				}
				return
			}
			if sc.Verdict != verdictInconclusive || !strings.HasPrefix(sc.Reason, tc.reasonPrefix) ||
				!strings.HasSuffix(sc.Reason, "; no hook from a split-pane teammate's process was recorded; pane text in "+tc.paneArtifacts[0]) {
				t.Errorf("verdict %s, reason %q", sc.Verdict, sc.Reason)
			}
			if got := readFile(t, filepath.Join(r.h.cfg.outDir, tc.paneArtifacts[0])); got != "history of %1\n"+tc.screen {
				t.Errorf("lead capture %q", got)
			}
		})
	}
}
