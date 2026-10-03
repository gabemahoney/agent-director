package realtmux_test

// advice_follow_kill_test.go: b.fji literal-follow test of kill's
// repeated-kill limitation (advice inventory C7) on real tmux, whose server
// exits once its last session ends.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// advKillCheckAgain is how long the caller waits before checking again, and
// advKillServerExit how long it keeps doing so before the test fails.
const (
	advKillCheckAgain = 200 * time.Millisecond
	advKillServerExit = 10 * time.Second
)

// TestAdviceFollow_C7_RepeatedKillWhileServerExits: a kill repeated right
// after the server's last session ended either succeeds, or gets one of the
// two named errors until, after waiting and checking again, it succeeds.
func TestAdviceFollow_C7_RepeatedKillWhileServerExits(t *testing.T) {
	// C7 kill manifest: "A repeated kill right after the last session on its tmux server ends can get ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits; the caller waits and checks again."
	const advice = "A repeated kill right after the last session on its tmux server ends can get " +
		"ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits; the caller waits and checks again."
	if v, ok := manifest.Lookup("kill"); !ok || !strings.Contains(v.Description, advice) {
		t.Fatalf("kill's Description lacks the advice %q", advice)
	}
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{}) // the server's only session
	c := f.open(t, 0, "")
	defer c.Close() //nolint:errcheck
	kill := func() (api.KillResult, error) { return c.Kill(api.KillParams{ClaudeInstanceID: r.InstanceID}) }
	if res, err := kill(); err != nil || !res.KillSent {
		t.Fatalf("first kill = %+v, %s; want success with kill_sent true", res, describe(err))
	}

	deadline := time.Now().Add(advKillServerExit)
	var answers []string
	for {
		res, err := kill()
		if err == nil {
			if res.KillSent {
				t.Errorf("repeated kill sent a kill; want kill_sent false")
			}
			break
		}
		name, _ := errnames.Classify(err)
		if name != "ErrTmuxUnresponsive" && name != "ErrTmuxNotAvailable" {
			t.Fatalf("repeated kill: %s; want success, ErrTmuxUnresponsive or ErrTmuxNotAvailable", describe(err))
		}
		answers = append(answers, name)
		if time.Now().After(deadline) {
			t.Fatalf("repeated kill still fails after %s (answers %v): %s", advKillServerExit, answers, describe(err))
		}
		time.Sleep(advKillCheckAgain)
		if st, err := c.Status(r.InstanceID); err != nil || st.State != "waiting" {
			t.Fatalf("status while checking again = %+v, %s; want waiting (kill never changes the row)", st, describe(err))
		}
	}
	t.Logf("answers before the repeated kill succeeded: %v", answers)
	assertProcs(t, true, r.Reply.PanePID)
	f.assertRowUnchanged(t, r.InstanceID, r.Before)
}
