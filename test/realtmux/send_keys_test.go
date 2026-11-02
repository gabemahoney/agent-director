package realtmux_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// b.qcf: pending Claude fallback must wait for raw input, SessionStart, and
// a 1.5 s settle before typing literal text and one Enter. The pane records
// bytes and fires the real hook; it does not emulate Claude's composer.
func TestSendKeysClaudeFallbackWaitsForRawModeRealTmux(t *testing.T) {
	const prompt = "Read worker-prompt.md first. Then read and follow brief-test-writer-regression.md"
	bin := buildHookCLI(t, t.TempDir())
	payload, err := filepath.Abs(sessionStartFixture)
	if err != nil {
		t.Fatal(err)
	}
	// The reader stays active during the hook, so early input cannot acquire
	// an artificially late timestamp. Both timestamps use Python's monotonic
	// clock. hook.start is a conservative lower bound on the state transition;
	// using hook completion would race send-keys observing the committed row.
	const recorder = `import json, os, pathlib, subprocess, sys, threading, time, tty
control = pathlib.Path(sys.argv[1])
mode, binary, payload = sys.argv[2:]
def wait_file(name):
    end = time.monotonic() + 30
    while not (control / name).exists():
        if time.monotonic() >= end:
            raise TimeoutError(name)
        time.sleep(0.01)
reads = open(control / "reads", "w", buffering=1)
def record():
    while True:
        data = os.read(0, 65536)
        if not data:
            return
        reads.write(json.dumps({"t": time.monotonic_ns(), "hex": data.hex()}) + "\n")
if mode == "raw-before-session-start":
    tty.setraw(0, when=0)
threading.Thread(target=record, daemon=True).start()
(control / "ready").touch()
if mode == "cooked-then-raw":
    wait_file("raw")
    tty.setraw(0, when=0)
(control / "raw-ready").touch()
wait_file("session-start")
with open(payload, "rb") as source:
    started = time.monotonic_ns()
    result = subprocess.run([binary, "hook"], stdin=source, capture_output=True, timeout=10)
finished = {"start": started, "end": time.monotonic_ns(), "exit": result.returncode,
            "stdout": result.stdout.decode(), "stderr": result.stderr.decode(), "parent": os.getpid()}
(control / "hook.tmp").write_text(json.dumps(finished))
os.replace(control / "hook.tmp", control / "hook.json")
threading.Event().wait(30)
`
	readBytes := func(t *testing.T, dir string) (first int64, data []byte) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "reads"))
		if err != nil {
			t.Fatal(err)
		}
		// The last record may still be in flight; parse only complete lines.
		for _, line := range strings.Split(string(raw[:strings.LastIndexByte(string(raw), '\n')+1]), "\n") {
			if line == "" {
				continue
			}
			var chunk struct {
				T   int64  `json:"t"`
				Hex string `json:"hex"`
			}
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				t.Fatal(err)
			}
			b, err := hex.DecodeString(chunk.Hex)
			if err != nil {
				t.Fatal(err)
			}
			if first == 0 {
				first = chunk.T
			}
			data = append(data, b...)
		}
		return first, data
	}
	for _, name := range []string{"cooked-then-raw", "raw-before-session-start"} {
		t.Run(name, func(t *testing.T) {
			f := newKillFix(t)
			dir := t.TempDir()
			mark := func(name string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			waitFile := func(name string) {
				t.Helper()
				waitFor(t, "recorder writes "+name, func() bool {
					_, err := os.Stat(filepath.Join(dir, name))
					return err == nil
				}, nil)
			}
			r := f.liveRow(t, killRowSpec{
				State:   store.StatePending,
				Opts:    []apitest.SpawnOption{apitest.WithAgent("claude")},
				Command: []string{"python3", "-c", recorder, dir, name, bin, payload},
			})
			waitFile("ready")
			_, log := newRecordingClient(t)
			c := f.open(t, 0, filepath.Join(log.dir, "tmux-recorder"))
			t.Cleanup(func() { _ = c.Close() })
			var result api.SendKeysResult
			var sendErr error
			done := make(chan struct{})
			go func() {
				result, sendErr = c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: prompt, AllowPending: true})
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
				waitForObserved(t, 30*time.Second, "send-keys goroutine exits", finished, nil)
			})
			terminalReads := 0
			wantReads := 1
			if name == "cooked-then-raw" {
				wantReads = 2 // The second probe proves the first read finished cooked.
			}
			waitFor(t, "send-keys reads the pane terminal", func() bool {
				// Read argv files directly: calls() requires all calls to have
				// finished, but send-keys is still running here.
				paths, err := filepath.Glob(filepath.Join(log.dir, "*.args"))
				if err != nil {
					t.Fatal(err)
				}
				terminalReads = 0
				for _, path := range paths {
					args, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasSuffix(string(args), "\x00#{pane_tty}\x00") {
						terminalReads++
					}
				}
				return terminalReads >= wantReads || finished()
			}, func() string { return fmt.Sprintf("terminal probes=%d", terminalReads) })
			if terminalReads < wantReads {
				t.Errorf("send-keys returned before observing terminal: probes=%d, want >=%d", terminalReads, wantReads)
			}
			mark("raw")
			waitFile("raw-ready")
			// Hold SessionStart back after a terminal probe. This interval is
			// the behaviour under test; barriers establish ordering under load.
			holdUntil := time.Now().Add(400 * time.Millisecond)
			waitFor(t, "pending observation interval", func() bool { return !time.Now().Before(holdUntil) }, nil)
			if _, got := readBytes(t, dir); len(got) != 0 {
				t.Errorf("input reached raw program before SessionStart while row pending: %q", got)
			}
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
			mark("session-start")
			waitFile("hook.json")
			var hook struct {
				Start, End     int64
				Exit, Parent   int
				Stdout, Stderr string
			}
			if err := json.Unmarshal([]byte(readFileTrim(t, filepath.Join(dir, "hook.json"))), &hook); err != nil {
				t.Fatal(err)
			}
			if hook.Exit != 0 || hook.Parent != r.Reply.PanePID || hook.Start <= 0 || hook.End < hook.Start {
				t.Fatalf("SessionStart did not run from the pane: %+v", hook)
			}
			row := readRow(t, f.DBPath, r.InstanceID)
			if row.State != store.StateWaiting || fmt.Sprint(row.PID) != fmt.Sprint(r.Reply.PanePID) || row.SessionID == nil {
				t.Fatalf("SessionStart not applied: state=%v pid=%v session=%v hook=%+v", row.State, row.PID, row.SessionID, hook)
			}
			waitForObserved(t, 30*time.Second, "send-keys returns after SessionStart and settle", finished, nil)
			// SessionStart fired, but no UserPromptSubmit confirms submission.
			want := api.SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: store.StateWaiting}
			if got := api.ErrDetails(sendErr); !errors.Is(sendErr, api.ErrSubmissionUnconfirmed) || got != want || result != (api.SendKeysResult{}) {
				t.Errorf("send-keys: result=%+v err=%v details=%+v; want ErrSubmissionUnconfirmed with details %+v", result, sendErr, got, want)
			}
			// Drain a test-owned barrier after SendKeys has returned, proving
			// there is no extra queued input beyond exactly prompt + one CR.
			barrier := newInstanceID("barrier")
			if err := newClient().SendKeysPane(r.RT.Socket, r.Reply.PaneID, barrier, true); err != nil {
				t.Fatal(err)
			}
			var first int64
			var got []byte
			waitFor(t, "program reads the barrier", func() bool {
				first, got = readBytes(t, dir)
				return strings.HasSuffix(string(got), barrier+"\r")
			}, func() string { return fmt.Sprintf("read %q", got) })
			if string(got) != prompt+"\r"+barrier+"\r" {
				t.Errorf("program read %q; want exactly the prompt then one CR before the barrier", got)
			}
			delay := time.Duration(first - hook.Start)
			t.Logf("first byte relative to SessionStart invocation: %s; hook duration=%s; details=%+v", delay, time.Duration(hook.End-hook.Start), api.ErrDetails(sendErr))
			if delay < 1500*time.Millisecond {
				t.Errorf("first byte arrived %s after SessionStart invocation; want at least 1.5s", delay)
			}
		})
	}

	for _, tc := range []struct{ name, script, enter string }{
		{"never-raw", "", "\n"},
		{"never-session-start", "stty raw -echo; ", "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("waits about 12 s for pending Claude readiness")
			}
			f := newKillFix(t)
			received := filepath.Join(t.TempDir(), "terminal-input")
			r := f.liveRow(t, killRowSpec{
				State:   store.StatePending,
				Opts:    []apitest.SpawnOption{apitest.WithAgent("claude")},
				Command: []string{"sh", "-c", tc.script + `exec cat > "$1"`, "capture", received},
			})
			waitExeced(t, r.Reply.PanePID, "cat")
			c := f.open(t, 0, "")
			t.Cleanup(func() { _ = c.Close() })
			started := time.Now()
			result, err := c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: prompt, AllowPending: true})
			elapsed := time.Since(started)
			details, _ := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
			want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StatePending, Reason: details.Reason}
			if !errors.Is(err, api.ErrSubmissionUnconfirmed) || details != want || details.Reason == "" || result != (api.SendKeysResult{}) {
				t.Errorf("send-keys without readiness: result=%+v err=%v details=%+v; want ErrSubmissionUnconfirmed with details %+v", result, err, api.ErrDetails(err), want)
			}
			if tc.name == "never-session-start" && elapsed < 12*time.Second {
				t.Errorf("send-keys returned after %s; want readiness refusal after the 12s bound", elapsed)
			}
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
			// The barrier follows everything send-keys typed. It detects both
			// cooked queued input and raw input, without looking at screen text.
			barrier := newInstanceID("barrier")
			if err := newClient().SendKeysPane(r.RT.Socket, r.Reply.PaneID, barrier, true); err != nil {
				t.Fatalf("type the barrier: %s", describe(err))
			}
			var got []byte
			waitFor(t, "program reads the barrier", func() bool {
				got, _ = os.ReadFile(received)
				return strings.HasSuffix(string(got), barrier+tc.enter)
			}, func() string { return fmt.Sprintf("read %q", got) })
			if string(got) != barrier+tc.enter {
				t.Errorf("input reached the terminal: program read %d bytes before the barrier: %q", len(got)-len(barrier)-1, got)
			}
			t.Logf("send-keys elapsed=%s details=%+v bytes before barrier=%d", elapsed, api.ErrDetails(err), len(got)-len(barrier)-1)
		})
	}
}
