package realtmux_test

// advice_follow_pane_test.go (b.fji E2, E3, E5; b.9o4): send-keys' and
// pause's advice after a text or Enter call that timed out or failed,
// followed literally on real tmux. A wrapper around tmux breaks one keys
// call; the agent's pane runs cat -, so its capture shows what the agent was
// given: the line typed (the tty's echo), then cat's copy once submitted.
// E15 (b.ob8) follows docs/usage.md's attach command block.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advAttachBlock is docs/usage.md's "Attach to a Spawn's pane" command block (E15).
const advAttachBlock = "```sh\nagent-director get --instance-id \"$id\"\n" +
	"tmux -u -S '<tmux_socket>' attach-session -t '<tmux_session_name>'\n```"

// advPaneActionTimeout is the action timeout of the wrapped client: the
// production default, so a broken call times out quickly.
const advPaneActionTimeout = config.DefaultActionTimeoutMs * time.Millisecond

// advPaneWrapper writes a tmux wrapper that runs the real tmux, except the
// first text call (send-keys -l) or Enter call (send-keys ... Enter) after
// arm(call, how), which is broken as how says: "deliver-hang" runs tmux, then
// hangs past the action timeout; "hang" hangs without running it; "fail"
// exits 1 with a reply agent-director does not recognise. Any other key send
// (pause's C-u) always runs tmux.
func advPaneWrapper(t *testing.T) (string, func(call, how string)) {
	t.Helper()
	if tmuxPath == "" {
		t.Skip("tmux is not on PATH")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
d='` + dir + `'
call=
case " $* " in *" send-keys "*)
	case " $* " in *" -l -- "*) call=text ;; *" Enter ") call=enter ;; esac ;;
esac
if [ -n "$call" ] && [ -f "$d/$call" ]; then
	how=$(cat "$d/$call")
	rm -f "$d/$call"
	case "$how" in
	deliver-hang) '` + tmuxPath + `' "$@"; exec sleep 60 ;;
	hang) exec sleep 60 ;;
	fail) echo 'advpane: injected failure' >&2; exit 1 ;;
	esac
fi
exec '` + tmuxPath + `' "$@"
`
	wrapper := filepath.Join(dir, "tmux-advpane")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux wrapper: %v", err)
	}
	return wrapper, func(call, how string) {
		if err := os.WriteFile(filepath.Join(dir, call), []byte(how), 0o600); err != nil {
			t.Fatalf("arm %s %s: %v", call, how, err)
		}
	}
}

// advPaneOpen opens the production client on f's store running tmuxCommand,
// with the fixture's query and create timeouts and advPaneActionTimeout.
func advPaneOpen(t *testing.T, f *killFix, tmuxCommand string) *api.Client {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfg,
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, clientTimeouts.Query.Milliseconds()),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, advPaneActionTimeout.Milliseconds()),
		apitest.TmuxInt(config.TmuxCreateTimeoutMs, clientTimeouts.Create.Milliseconds()))
	c, err := api.New(api.Options{StorePath: f.DBPath, ConfigPath: cfg, TmuxCommand: tmuxCommand})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// advPaneCatRow seeds a waiting row whose agent pane runs cat - and opens the
// production client on a wrapper whose arm breaks one keys call.
func advPaneCatRow(t *testing.T) (*killFix, killRow, *api.Client, func(call, how string)) {
	t.Helper()
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{Command: []string{"cat", "-"}})
	wrapper, arm := advPaneWrapper(t)
	return f, r, advPaneOpen(t, f, wrapper), arm
}

// advPaneAssertAdvice fails unless err is ErrTmuxUnresponsive carrying advice word for word.
func advPaneAssertAdvice(t *testing.T, err error, advice string) {
	t.Helper()
	if !errors.Is(err, api.ErrTmuxUnresponsive) {
		t.Fatalf("refusal = %s; want ErrTmuxUnresponsive carrying %q", describe(err), advice)
	}
	if !strings.Contains(err.Error(), advice) {
		t.Errorf("refusal = %s; want it to carry %q", describe(err), advice)
	}
}

// advPaneWaitShows waits until the capture of r's agent pane, its trailing
// newlines trimmed, is want.
func advPaneWaitShows(t *testing.T, f *killFix, r killRow, want, what string) {
	t.Helper()
	var last string
	waitFor(t, fmt.Sprintf("pane %s shows %q: %s", r.Reply.PaneID, want, what),
		func() bool { last = f.capture(t, r.Reply.PaneID); return last == want },
		func() string { return fmt.Sprintf("capture %q, want %q", last, want) })
}

// advPaneKeysCase is one broken keys call: which call, how it breaks and the
// advice its refusal carries.
type advPaneKeysCase struct {
	name, call, how, advice string
}

// send-keys' keys-failure advice (b.9o4), as the unit tier pins it.
const (
	advSendKeysTextTimeout  = "the keys may have been delivered; read-pane; if the text is typed, send-keys with empty text, otherwise the same send-keys"
	advSendKeysEnterTimeout = "the keys may have been delivered; the text may be typed but not submitted; send-keys with empty text submits it"
	advSendKeysNotSubmitted = "the text may be typed but not submitted; send-keys with empty text submits it"
)

// TestAdviceFollow_E2_SendKeysTextTimeoutReadPaneThenEmptyTextRealTmux: E2
// "the keys may have been delivered; read-pane; if the text is typed,
// send-keys with empty text, otherwise the same send-keys" after the text
// call timed out.
func TestAdviceFollow_E2_SendKeysTextTimeoutReadPaneThenEmptyTextRealTmux(t *testing.T) {
	advPaneFollowRealTmux(t, []advPaneKeysCase{
		{name: "text call timed out before reaching the pane", call: "text", how: "hang", advice: advSendKeysTextTimeout},
		{name: "text call timed out after typing the text", call: "text", how: "deliver-hang", advice: advSendKeysTextTimeout},
	}, func(t *testing.T, c *api.Client, id, text string) {
		read, err := c.ReadPane(api.ReadPaneParams{InstanceID: id})
		if err != nil {
			t.Fatalf("read-pane: %s; want the agent's pane", describe(err))
		}
		p := api.SendKeysParams{InstanceID: id, Text: text}
		if strings.Contains(read.Pane, text) {
			p.Text = ""
		}
		if _, err := c.SendKeys(p); err != nil {
			t.Fatalf("send-keys of %q after read-pane: %s; want delivery", p.Text, describe(err))
		}
	})
}

// TestAdviceFollow_E3_SendKeysEnterFailedEmptyTextSubmitsRealTmux: E3 "the
// text may be typed but not submitted; send-keys with empty text submits it"
// (after a timeout, "the keys may have been delivered; " first) after the
// Enter call failed.
func TestAdviceFollow_E3_SendKeysEnterFailedEmptyTextSubmitsRealTmux(t *testing.T) {
	advPaneFollowRealTmux(t, []advPaneKeysCase{
		{name: "Enter timed out before reaching the pane", call: "enter", how: "hang", advice: advSendKeysEnterTimeout},
		{name: "Enter failed, the follow-up lookup found the session", call: "enter", how: "fail",
			advice: advSendKeysNotSubmitted},
		{name: "Enter timed out after submitting the text", call: "enter", how: "deliver-hang",
			advice: advSendKeysEnterTimeout},
	}, func(t *testing.T, c *api.Client, id, _ string) {
		if _, err := c.SendKeys(api.SendKeysParams{InstanceID: id}); err != nil {
			t.Fatalf("send-keys with empty text: %s; want Enter delivered", describe(err))
		}
	})
}

// advPaneFollowRealTmux breaks each case's keys call once in send-keys of a
// new marker, checks the refusal's advice and follows it (follow); cat's
// pane must then show the marker submitted once, typed and copied
// ("<text>\n<text>"). An Enter-only send on an empty line makes cat copy an
// empty line, which the capture's trailing-newline trim does not show;
// Claude Code submits nothing on an Enter in an empty input, so the agent
// gets the text once either way.
func advPaneFollowRealTmux(t *testing.T, cases []advPaneKeysCase, follow func(t *testing.T, c *api.Client, id, text string)) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, r, c, arm := advPaneCatRow(t)
			text := paneMarker(t)
			arm(tc.call, tc.how)

			_, err := c.SendKeys(api.SendKeysParams{InstanceID: r.InstanceID, Text: text})
			advPaneAssertAdvice(t, err, tc.advice)

			follow(t, c, r.InstanceID, text)
			advPaneWaitShows(t, f, r, text+"\n"+text, "submitted once (typed, then copied by cat)")
		})
	}
}

// TestAdviceFollow_E5_PauseKeysFailedRetryLaterRealTmux: E5 "the keys may
// have been delivered; retry later" (and pause's Enter failures, "the text
// may be typed but not submitted; retry later") with /exit left typed; the
// retried pause clears the line (C-u, a key) before typing, so cat gets /exit
// once, never /exit/exit.
func TestAdviceFollow_E5_PauseKeysFailedRetryLaterRealTmux(t *testing.T) {
	for _, tc := range []advPaneKeysCase{
		{name: "/exit text call timed out after typing /exit", call: "text", how: "deliver-hang",
			advice: "the keys may have been delivered; retry later"},
		{name: "Enter timed out before reaching the pane", call: "enter", how: "hang",
			advice: "the keys may have been delivered; the text may be typed but not submitted; retry later"},
		{name: "Enter failed, the follow-up lookup found the session", call: "enter", how: "fail",
			advice: "the text may be typed but not submitted; retry later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r, c, arm := advPaneCatRow(t)
			arm(tc.call, tc.how)
			// cat never ends the row: a cancelled context ends the wait at once,
			// so context.Canceled means C-u, /exit and Enter went through.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			pause := func() error { _, err := c.Pause(ctx, api.PauseParams{InstanceID: r.InstanceID}); return err }

			advPaneAssertAdvice(t, pause(), tc.advice)

			if err := pause(); !errors.Is(err, context.Canceled) {
				t.Fatalf("re-issued pause: %s; want /exit and Enter delivered, then the (cancelled) wait", describe(err))
			}
			advPaneWaitShows(t, f, r, "/exit\n/exit", "/exit submitted once (typed, then copied by cat), never /exit/exit")
		})
	}
}

// TestAdviceFollow_E15_AttachToPaneRealTmux: E15 (b.ob8), docs/usage.md's
// "Attach to a Spawn's pane", given for every agent: get's tmux_socket and
// tmux_session_name, put into the block's attach command, attach a terminal
// to the row's own session, and the client exits 0 once detached. The
// terminal is a pane of a second server, with TMUX unset as in a plain one.
func TestAdviceFollow_E15_AttachToPaneRealTmux(t *testing.T) {
	usage, err := os.ReadFile("../../docs/usage.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(usage), "\n## Attach to a Spawn's pane\n")
	section, _, _ = strings.Cut(section, "\n## ")
	if !strings.Contains(section, advAttachBlock) {
		t.Fatalf("docs/usage.md \"Attach to a Spawn's pane\" lacks %q; recheck its literal follow", advAttachBlock)
	}
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{})
	c := f.open(t, 0, "")
	t.Cleanup(func() { _ = c.Close() })
	row, err := c.Get(r.InstanceID)
	if err != nil {
		t.Fatalf("get: %s", describe(err))
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Socket string `json:"tmux_socket"`
		Name   string `json:"tmux_session_name"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Socket == "" || got.Name == "" {
		t.Fatalf("get carries tmux_socket %q and tmux_session_name %q (%v); want both", got.Socket, got.Name, err)
	}
	_, attach, _ := strings.Cut(strings.TrimSuffix(advAttachBlock, "\n```"), "\"$id\"\n")
	attach = strings.NewReplacer("<tmux_socket>", got.Socket, "<tmux_session_name>", got.Name).Replace(attach)
	status := filepath.Join(t.TempDir(), "status")
	exit := func() string { b, _ := os.ReadFile(status); return string(b) }

	f.fresh(t).must(t, "new-session", "-d", "-x", "80", "-y", "24", "--", "env", "-u", "TMUX", "sh", "-c", attach+"; echo $? >'"+status+"'")

	var clients string
	waitFor(t, "one client attached to the row's session", func() bool {
		clients = r.RT.run(t, "list-clients", "-F", "#{session_id}").Stdout
		return clients == r.Reply.SessionID+"\n"
	}, func() string { return fmt.Sprintf("clients %q, attach exit %q", clients, exit()) })
	r.RT.must(t, "detach-client", "-s", r.Reply.SessionID)
	waitFor(t, "the attach to exit 0 once detached", func() bool { return exit() == "0\n" },
		func() string { return fmt.Sprintf("attach exit %q", exit()) })
}
