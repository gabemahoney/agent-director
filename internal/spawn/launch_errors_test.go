package spawn

import (
	"errors"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// TestLaunchTimeoutRetrySentence pins plain spawn's launch-timeout ending by
// minted (b.1qq): a caller-supplied id's names the opted-in retry, a minted id's does not.
func TestLaunchTimeoutRetrySentence(t *testing.T) {
	const rule = "; the session may have been created; the row stays pending; do not retry until get shows the row ended or missing"
	failures := []struct {
		name   string
		script tmuxfix.Script
	}{
		{"timeout", tmuxfix.Script{Failure: tmux.FailTimeout}},
		{"unrecognised reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "odd", ExitStatus: 1, HadStdout: true}},
	}
	ids := []struct {
		name   string
		minted bool
		ending string
	}{
		{"caller-supplied id", false, rule + "; then a retry with this id uses the reuse opt-in reuse_finished " +
			"(--reuse-finished on the CLI), since a plain spawn of the id now collides"},
		{"minted id", true, rule},
	}
	for _, f := range failures {
		for _, id := range ids {
			t.Run(f.name+"/"+id.name, func(t *testing.T) {
				e := newLaunchEnv(t)
				e.minted = id.minted
				e.rec.Script(tmuxfix.AnySocket, f.script, tmux.CallCreate)

				_, _, err := e.launch()

				if !errors.Is(err, tmux.ErrTmuxUnresponsive) {
					t.Fatalf("Launch err = %v; want ErrTmuxUnresponsive", err)
				}
				const lead = "tmux: unresponsive: spawn of instance id-launch-1: tmux session creation failed: "
				if msg := err.Error(); !strings.HasPrefix(msg, lead) || !strings.HasSuffix(msg, id.ending) {
					t.Errorf("err = %q\nwant %q ... %q", msg, lead, id.ending)
				}
				if id.minted && strings.Contains(err.Error(), "reuse opt-in") {
					t.Errorf("err = %q; a minted id's retry mints a new one, so no reuse opt-in", err.Error())
				}
			})
		}
	}
}

// TestInstanceCreateFailedError pins plain spawn's vanished-holder text
// (b.1qq): CreateFailedError's, led by "instance <id>: " after the sentinel.
func TestInstanceCreateFailedError(t *testing.T) {
	ce := &tmux.CallError{Call: tmux.CallCreate, Failure: tmux.FailDuplicate}
	const tail = `tmux session "bot-1": tmux session creation failed: duplicate session; the new row was ended; RETRY`

	err := InstanceCreateFailedError(ce, "id-1", "bot-1", "the new row was ended; RETRY")

	if want := "tmux: new-session failed: instance id-1: " + tail; err.Error() != want {
		t.Errorf("InstanceCreateFailedError = %q\nwant %q", err.Error(), want)
	}
	if plain := CreateFailedError(ce, "bot-1", "the new row was ended; RETRY").Error(); plain != "tmux: new-session failed: "+tail {
		t.Errorf("CreateFailedError = %q\nwant the same tail %q with no instance lead", plain, tail)
	}
	for _, s := range catalogued {
		if errors.Is(err, s) != (s == tmux.ErrTmuxSessionCreate) {
			t.Errorf("errors.Is(err, %v) = %v; want only ErrTmuxSessionCreate", s, errors.Is(err, s))
		}
	}
}
