package realtmux_test

// advice_follow_pane_test.go (b.fji E2, E3): send-keys' "retry later" after
// a text or Enter call that timed out or failed, followed literally on real
// tmux. A wrapper around tmux breaks one keys call; the agent's pane runs
// cat -, so its capture shows what the agent was given.

import (
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

// advPaneActionTimeout is the action timeout of the wrapped client: the
// production default, so a broken call times out quickly.
const advPaneActionTimeout = config.DefaultActionTimeoutMs * time.Millisecond

// advPaneWrapper writes a tmux wrapper that runs the real tmux, except the
// first text call (send-keys -l) or Enter call (send-keys ... Enter) after
// arm(call, how), which is broken as how says: "deliver-hang" runs tmux, then
// hangs past the action timeout; "hang" hangs without running it; "fail"
// exits 1 with a reply agent-director does not recognise.
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

// advPaneKeysCase is one broken keys call: which call, how it breaks, the
// advice its refusal carries and why the literal retry is known not to work
// ("" when it works).
type advPaneKeysCase struct {
	name, call, how, advice, broken string
}

// advPaneRetypes is why a retry after typed, unsubmitted text is known not to work.
const advPaneRetypes = "the retry types the text again after the unsubmitted copy and submits both as one line"

// TestAdviceFollow_E2_SendKeysTimeoutRetryLaterRealTmux: E2 "the keys may
// have been delivered; retry later" after the text call timed out.
func TestAdviceFollow_E2_SendKeysTimeoutRetryLaterRealTmux(t *testing.T) {
	const advice = "the keys may have been delivered; retry later"
	advPaneRetryRealTmux(t, "E2", []advPaneKeysCase{
		{name: "text call timed out before reaching the pane", call: "text", how: "hang", advice: advice},
		{name: "text call timed out after typing the text", call: "text", how: "deliver-hang", advice: advice,
			broken: advPaneRetypes},
	})
}

// TestAdviceFollow_E3_SendKeysEnterFailedRetryLaterRealTmux: E3 "the text
// may be typed but not submitted; retry later" after the Enter call failed.
func TestAdviceFollow_E3_SendKeysEnterFailedRetryLaterRealTmux(t *testing.T) {
	const advice = "the text may be typed but not submitted; retry later"
	advPaneRetryRealTmux(t, "E3", []advPaneKeysCase{
		{name: "Enter failed, the follow-up lookup found the session", call: "enter", how: "fail", advice: advice,
			broken: advPaneRetypes},
		{name: "Enter timed out after submitting the text", call: "enter", how: "deliver-hang", advice: advice,
			broken: "the text was submitted, and the retry submits it a second time"},
	})
}

// advPaneRetryRealTmux breaks each case's keys call once, then re-issues the
// same send-keys, after which cat's pane must show the text once, typed and
// copied ("<text>\n<text>"). A known-broken case is gated before its session
// starts: its advice phrase is pinned at the unit tier
// (pkg/api/advice_follow_pane_test.go), so a skip costs no timeout here.
func advPaneRetryRealTmux(t *testing.T, id string, cases []advPaneKeysCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.broken != "" {
				knownBrokenAdvice(t, id, tc.broken)
			}
			f := newKillFix(t)
			r := f.liveRow(t, killRowSpec{Command: []string{"cat", "-"}})
			wrapper, arm := advPaneWrapper(t)
			c := advPaneOpen(t, f, wrapper)
			text := paneMarker(t)
			send := func() error {
				_, err := c.SendKeys(api.SendKeysParams{ClaudeInstanceID: r.InstanceID, Text: text})
				return err
			}
			arm(tc.call, tc.how)

			err := send()
			if !errors.Is(err, api.ErrTmuxUnresponsive) || !strings.Contains(err.Error(), tc.advice) {
				t.Fatalf("send-keys = %s; want ErrTmuxUnresponsive carrying %q", describe(err), tc.advice)
			}

			if err := send(); err != nil {
				t.Fatalf("re-issued send-keys: %s; want delivery", describe(err))
			}
			want := text + "\n" + text
			var last string
			waitFor(t, fmt.Sprintf("pane %s shows %q submitted once (typed, then copied by cat)", r.Reply.PaneID, text),
				func() bool { last = f.capture(t, r.Reply.PaneID); return last == want },
				func() string { return fmt.Sprintf("capture %q, want %q", last, want) })
		})
	}
}
