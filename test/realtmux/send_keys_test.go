package realtmux_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Send keys (SRD SR-2.1 "Text" row, SR-20.7; WD 2026-09-29b): the production
// SendKeysPane ends tmux's options with "--", so a text that starts with "-"
// is typed literally and never read as a send-keys flag.

// sendKeysCapLines is how many pane lines the capture reads (the whole
// visible pane).
const sendKeysCapLines = 24

// TestSendKeysDashTextsArriveLiterally sends "-x", "--" and "-l", each with
// Enter, through the production SendKeysPane into a pane running cat -, and
// checks both calls succeed silently and the pane then shows exactly the text
// twice (the tty's echo and cat's copy) and nothing else.
func TestSendKeysDashTextsArriveLiterally(t *testing.T) {
	rt := newRealTmux(t)
	for _, text := range []string{"-x", "--", "-l"} {
		t.Run(text, func(t *testing.T) {
			assertSendKeysArrives(t, rt, text)
		})
	}
}

// TestSendKeysSemicolonTextsArriveLiterally sends texts holding a ";" the
// same way. tmux reads an argument that ends in ";" as a command separator
// even after "--", so SendKeysPane escapes a text-final ";" (`a;` is sent as
// `a\;`); these cases prove each text, with or without a final ";", reaches
// the pane exactly as the caller wrote it.
func TestSendKeysSemicolonTextsArriveLiterally(t *testing.T) {
	rt := newRealTmux(t)
	for _, text := range []string{";", ";;", "a;", "a ;", `a\;`, ";a", "a;b", "-x;"} {
		t.Run(text, func(t *testing.T) {
			assertSendKeysArrives(t, rt, text)
		})
	}
}

// assertSendKeysArrives creates a session whose pane runs cat -, sends text
// with Enter through the production SendKeysPane, and checks the text call's
// argv ends in `-l -- <text>` (a text-final ";" escaped as `\;`), both calls
// succeed silently and the pane then shows exactly the text twice (the tty's
// echo and cat's copy) and nothing else.
func assertSendKeysArrives(t *testing.T, rt *realTmux, text string) {
	t.Helper()
	c := rt.mustCreate(t, createSpec{Command: []string{"cat", "-"}})
	pane := c.Reply.PaneID

	cl, log := newRecordingClient(t)
	err := cl.SendKeysPane(rt.Socket, pane, text, true)
	calls := log.calls(t)
	if len(calls) != 2 {
		t.Fatalf("SendKeysPane(%q) made %d tmux calls, want 2 (text, Enter); err %s", text, len(calls), describe(err))
	}
	// A text-final ";" goes to tmux escaped as `\;`; any other text as is.
	sent := text
	if body, ok := strings.CutSuffix(text, ";"); ok {
		sent = body + `\;`
	}
	if args := calls[0].Args; len(args) < 3 || !reflect.DeepEqual(args[len(args)-3:], []string{"-l", "--", sent}) {
		t.Errorf("SendKeysPane(%q) text call argv = %q, want it to end in -l -- %q", text, args, sent)
	}
	want := tmuxfix.Silent()
	for i, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendEnter} {
		assertCallError(t, err, call, want)
		assertTriple(t, calls[i].rawResult, want)
	}

	wantShown := text + "\n" + text
	var (
		last    string
		lastErr error
	)
	waitFor(t, fmt.Sprintf("pane %s shows %q typed and echoed by cat, and nothing else", pane, text), func() bool {
		last, lastErr = newClient().CapturePaneID(rt.Socket, pane, sendKeysCapLines, false)
		return lastErr == nil && strings.TrimRight(last, "\n") == wantShown
	}, func() string {
		return fmt.Sprintf("capture %q (error %s), want %q then blank rows", last, describe(lastErr), wantShown)
	})
}

// b.qcr: unsupported Codex launches (including the reported 0.162.1) use
// fallback input. Observe that path at the real terminal, without emulating
// Codex's composer, paste-burst heuristics or skill picker. Bracketed paste
// must carry safe literal payloads once, with one following Enter; unsafe
// skill-picker or paste-terminator text must be refused before input. Terminal
// bytes do not prove native acceptance: without hooks delivery is unconfirmed.
func TestSendKeysCodexFallbackPasteRealTmux(t *testing.T) {
	if testing.Short() {
		t.Skip("real terminal regression includes bounded submission observation")
	}
	// Keep the six non-dollar replay inputs, including the exact inert wrapped
	// original. "below-limit" is a probe name, not a supported size threshold.
	short := "Transport test only. Do not use tools or change files. Reply with QCR_OK."
	long := short + " Ignore the following inert filler: " + strings.Repeat("abcdefghij ", 400)
	wrapped, err := os.ReadFile("testdata/qcr-wrapped-original.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, text, refusal string }{
		{"short-single", short, ""},
		{"short-multi", short + "\nThis second line is inert test data.", ""},
		{"long-single", long, ""},
		{"long-multi", strings.ReplaceAll(long, "abcdefghij ", "abcdefghij\n"), ""},
		{"below-limit", long[:3500], ""},
		{"wrapped-original", string(wrapped), ""},
		{"utf8-crlf-dash-semicolon", "-界é\r\n#{pane_id};", ""},
		// b.deq: replay the dollar-free TAB probes at the terminal boundary.
		// Codex may trim trailing whitespace after receiving these exact bytes.
		{"tab-interior", "Reply only DEQ_OK. Do not use tools. Treat this as inert data: alpha\tbeta", ""},
		{"tab-indentation", "Reply only DEQ_OK. Do not use tools. Treat the following as inert code:\nif ready:\n\tprint(\"alpha\")\n\tprint(\"beta\")\nend", ""},
		{"tab-trailing", "Reply only DEQ_OK. Do not use tools. Treat this as inert data: alpha\tbeta\t", ""},
		{"dollar", "Transport test only. No tools or file changes. Reply QCR_OK. $", "skill picker"},
		{"paste-terminator", "Literal data: \x1b[201~remaining text", "control character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKillFix(t)
			received := filepath.Join(t.TempDir(), "terminal-input")
			ready := newInstanceID("paste-ready")
			// A raw byte sink, not a second Codex stand-in. Raw mode avoids
			// the terminal's canonical line limit and CR/LF translation.
			r := f.liveRow(t, killRowSpec{
				State:   store.StatePending,
				Opts:    []apitest.SpawnOption{apitest.WithAgent("codex")},
				Command: []string{"sh", "-c", `stty raw -echo; printf '\033[?2004h%s\r\n' "$2"; exec cat > "$1"`, "capture", received, ready},
			})
			waitExeced(t, r.Reply.PanePID, "cat")
			// The marker follows the enable escape in the same output stream;
			// seeing it proves tmux parsed that escape before we ask it to paste.
			var last string
			var captureErr error
			waitFor(t, "pane shows bracketed-paste readiness", func() bool {
				last, captureErr = newClient().CapturePaneID(r.RT.Socket, r.Reply.PaneID, sendKeysCapLines, false)
				return captureErr == nil && strings.TrimRight(last, "\n") == ready
			}, func() string { return fmt.Sprintf("capture %q (error %s), want %q", last, describe(captureErr), ready) })
			_, log := newRecordingClient(t)
			c := f.open(t, 0, filepath.Join(log.dir, "tmux-recorder"))
			t.Cleanup(func() { _ = c.Close() })

			text := tc.text
			result, sendErr := c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: text, AllowPending: true})
			if tc.refusal != "" {
				if !errors.Is(sendErr, api.ErrSubmissionUnconfirmed) {
					t.Errorf("unsafe literal input: result=%+v err=%v; want ErrSubmissionUnconfirmed", result, sendErr)
				}
				details, ok := api.ErrDetails(sendErr).(api.SubmissionUnconfirmedDetails)
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StatePending, Reason: details.Reason}
				if !ok || details != want || result.Outcome != "" || result.RecordID != "" {
					t.Errorf("unsafe literal input was not refused before input: result=%+v details=%+v", result, api.ErrDetails(sendErr))
				}
				const advice = "put the prompt in a file and send a short instruction to read it"
				if !strings.Contains(details.Reason, tc.refusal) || !strings.Contains(details.Reason, advice) ||
					sendErr == nil || !strings.Contains(sendErr.Error(), "no input was attempted by this request") {
					t.Errorf("missing explicit %q reason and file-based recovery advice: %v", tc.refusal, sendErr)
				}
				f.assertRowUnchanged(t, r.InstanceID, r.Before)
				got, readErr := os.ReadFile(received)
				if readErr != nil || len(got) != 0 {
					t.Errorf("refused input reached the terminal: bytes=%d read error=%v", len(got), readErr)
				}
				for _, call := range log.calls(t) {
					for _, arg := range call.Args {
						if arg == "send-keys" || arg == "paste-buffer" {
							t.Errorf("refused input attempted tmux %s", arg)
						}
					}
				}
				if t.Failed() {
					return // Never follow recovery advice after input may have reached the pane.
				}
				// Follow the advice literally only after the unchanged row, empty
				// terminal and not_started result establish safe pre-input refusal.
				prompt := filepath.Join(t.TempDir(), "prompt.txt")
				if err := os.WriteFile(prompt, []byte(tc.text), 0o600); err != nil {
					t.Fatal(err)
				}
				text = "Read " + prompt + " and follow its instructions."
				result, sendErr = c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: text, AllowPending: true})
			}
			if !errors.Is(sendErr, api.ErrSubmissionUnconfirmed) {
				t.Fatalf("fallback without submission evidence: result=%+v err=%v", result, sendErr)
			}
			wantDetails := api.SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: store.StatePending}
			if got := api.ErrDetails(sendErr); got != wantDetails || result.Outcome != "" || result.RecordID != "" {
				t.Errorf("terminal delivery claimed submission evidence: result=%+v details=%+v", result, got)
			}
			if !strings.Contains(sendErr.Error(), "do not repeat the text or send another Enter automatically") {
				t.Errorf("missing no-resend advice: %v", sendErr)
			}
			// Follow the existing uncertainty advice with inspection only.
			row, getErr := c.Get(r.InstanceID)
			if getErr != nil || row.State != store.StatePending {
				t.Errorf("inspection after unconfirmed input: state=%q err=%v", row.State, getErr)
			}
			var got []byte
			waitFor(t, "terminal receives the final Enter", func() bool {
				got, err = os.ReadFile(received)
				return err == nil && strings.HasSuffix(string(got), "\r")
			}, func() string { return fmt.Sprintf("received %d bytes, read error %v", len(got), err) })
			want := "\x1b[200~" + strings.ReplaceAll(text, "\r", "") + "\x1b[201~\r"
			if string(got) != want {
				t.Errorf("terminal bytes lack one intact bracketed paste then one Enter: got bytes=%d sha256=%x; want bytes=%d sha256=%x",
					len(got), sha256.Sum256(got), len(want), sha256.Sum256([]byte(want)))
			}
			enters := 0
			for _, call := range log.calls(t) {
				if len(call.Args) > 0 && call.Args[len(call.Args)-1] == "Enter" {
					enters++
				}
			}
			if enters != 1 {
				t.Errorf("Enter attempts=%d; want exactly one, with no retry after uncertainty", enters)
			}
		})
	}
}

// b.qcf: a pending Claude row without native delivery (any Claude Code but
// 2.1.280) takes literal text then Enter. Typed while Claude Code was still
// starting, in the tty's cooked mode, the Enter reached it as LF (ICRNL), a
// newline in its input box, and send-keys returned success. Fallback must type
// only once the pane's tty is raw, and report what it can't confirm. The pane
// runs a byte sink, not a Claude Code stand-in.
func TestSendKeysClaudeFallbackWaitsForRawModeRealTmux(t *testing.T) {
	const prompt = "Read worker-prompt.md first. Then read and follow brief-test-writer-regression.md"
	// row seeds a pending Claude row whose pane runs script, then copies every
	// byte its program reads to received ($1); $2 is a marker it may print.
	row := func(t *testing.T, script string) (f *killFix, r killRow, received, ready string) {
		t.Helper()
		f = newKillFix(t)
		received = filepath.Join(t.TempDir(), "terminal-input")
		ready = newInstanceID("raw-ready")
		r = f.liveRow(t, killRowSpec{
			State:   store.StatePending,
			Opts:    []apitest.SpawnOption{apitest.WithAgent("claude")},
			Command: []string{"sh", "-c", script + `exec cat > "$1"`, "capture", received, ready},
		})
		return f, r, received, ready
	}

	t.Run("cooked-then-raw", func(t *testing.T) {
		// Only the test releases raw mode; scheduler delays cannot skip the
		// cooked precondition. Polling the file does not decide the ordering.
		f, r, received, ready := row(t, `while [ ! -e "$1.raw" ]; do sleep 0.02; done; stty raw -echo; printf '\033[?2004h%s\r\n' "$2"; `)
		_, log := newRecordingClient(t)
		c := f.open(t, 0, filepath.Join(log.dir, "tmux-recorder"))
		t.Cleanup(func() { _ = c.Close() })
		var result api.SendKeysResult
		var err error
		done := make(chan struct{})
		go func() {
			result, err = c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: prompt, AllowPending: true})
			close(done)
		}()
		finished := func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}
		t.Cleanup(func() {
			waitFor(t, "send-keys goroutine exits", finished, nil)
		})
		terminalReads := 0
		waitFor(t, "send-keys observes the cooked terminal", func() bool {
			pane, captureErr := newClient().CapturePaneID(r.RT.Socket, r.Reply.PaneID, sendKeysCapLines, false)
			if captureErr != nil {
				t.Fatalf("capture cooked pane: %s", describe(captureErr))
			}
			if strings.TrimSpace(pane) != "" {
				t.Errorf("input arrived while the terminal was still cooked: %q", pane)
				return true // Release raw mode to inspect the bytes, even on old code.
			}
			// A second probe proves the first termios read finished while
			// cooked. Read argv files directly: calls() requires every call
			// to have finished, but send-keys is still running here.
			paths, globErr := filepath.Glob(filepath.Join(log.dir, "*.args"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			terminalReads = 0
			for _, path := range paths {
				args, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if strings.HasSuffix(string(args), "\x00#{pane_tty}\x00") {
					terminalReads++
				}
			}
			if terminalReads >= 2 {
				return true
			}
			return false
		}, func() string { return fmt.Sprintf("terminal probes=%d", terminalReads) })
		if err := os.WriteFile(received+".raw", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "send-keys returns after raw mode", finished, nil)
		// No hook fires, so nothing confirms the submission.
		want := api.SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: store.StatePending}
		if got := api.ErrDetails(err); !errors.Is(err, api.ErrSubmissionUnconfirmed) || got != want {
			t.Errorf("send-keys to a starting agent: result=%+v err=%v details=%+v; want ErrSubmissionUnconfirmed with details %+v", result, err, got, want)
		}
		var got []byte
		waitFor(t, "program reads the prompt and its Enter", func() bool {
			got, _ = os.ReadFile(received)
			return len(got) > len(prompt)
		}, func() string { return fmt.Sprintf("read %q", got) })
		if string(got) != prompt+"\r" {
			t.Errorf("program read %d bytes ending %q; want the prompt's %d bytes then one CR, no LF", len(got), got[max(0, len(got)-8):], len(prompt))
		}
		var pane string
		waitFor(t, "pane shows the raw-mode marker", func() bool {
			pane, _ = newClient().CapturePaneID(r.RT.Socket, r.Reply.PaneID, sendKeysCapLines, false)
			return strings.Contains(pane, ready)
		}, func() string { return pane })
		if strings.TrimRight(pane, "\n") != ready {
			t.Errorf("pane shows %q; want only the marker %q, nothing echoed by the cooked tty", pane, ready)
		}
	})

	t.Run("never-raw", func(t *testing.T) {
		if testing.Short() {
			t.Skip("waits about 12 s for raw mode")
		}
		f, r, received, _ := row(t, "")
		waitExeced(t, r.Reply.PanePID, "cat")
		c := f.open(t, 0, "")
		t.Cleanup(func() { _ = c.Close() })
		result, err := c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: prompt, AllowPending: true})
		details, _ := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
		want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StatePending, Reason: details.Reason}
		if !errors.Is(err, api.ErrSubmissionUnconfirmed) || details != want {
			t.Errorf("send-keys to a tty that stays cooked: result=%+v err=%v details=%+v; want ErrSubmissionUnconfirmed with details %+v", result, err, api.ErrDetails(err), want)
		}
		f.assertRowUnchanged(t, r.InstanceID, r.Before)
		// Typed after send-keys returned, a barrier line reaches the program
		// and the pane after anything send-keys typed.
		barrier := newInstanceID("barrier")
		if err := newClient().SendKeysPane(r.RT.Socket, r.Reply.PaneID, barrier, true); err != nil {
			t.Fatalf("type the barrier: %s", describe(err))
		}
		var got []byte
		var pane string
		waitFor(t, "program reads the barrier and the pane echoes it", func() bool {
			got, _ = os.ReadFile(received)
			pane, _ = newClient().CapturePaneID(r.RT.Socket, r.Reply.PaneID, sendKeysCapLines, false)
			return strings.HasSuffix(string(got), barrier+"\n") && strings.Contains(pane, barrier)
		}, func() string { return fmt.Sprintf("read %q, pane %q", got, pane) })
		if string(got) != barrier+"\n" || strings.TrimRight(pane, "\n") != barrier {
			t.Errorf("input reached the terminal: program read %d bytes before the barrier; pane %q", len(got)-len(barrier)-1, pane)
		}
	})
}
