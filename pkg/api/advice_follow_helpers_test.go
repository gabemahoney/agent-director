package api_test

// advice_follow_helpers_test.go holds the helpers b.fji's literal-follow
// tests (advice_follow_*_test.go) share: the advice checks (an error's
// description, a Go doc comment, a manifest text), the wait the pending-row
// advice prescribes, the "duplicate session" arrangements B10 and A12 follow,
// a one-shot hook on a tmux call and small seeds.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// adviceAssertPhrase fails unless err's description carries phrase word for word.
func adviceAssertPhrase(t *testing.T, err error, phrase string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil; want a description carrying %q", phrase)
	}
	if !strings.Contains(err.Error(), phrase) {
		t.Errorf("description %q\nwant it to carry %q", err.Error(), phrase)
	}
}

// adviceAssertAdvice fails unless err matches want and no other catalogued
// error, and its description carries advice word for word.
func adviceAssertAdvice(t *testing.T, err, want error, advice string) {
	t.Helper()
	assertOneSentinel(t, err, want)
	adviceAssertPhrase(t, err, advice)
}

// adviceAssertGoDoc fails unless the doc comment of the func, var or const
// name declared in file (in pkg/api), its whitespace collapsed, carries advice.
func adviceAssertGoDoc(t *testing.T, file, name, advice string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var doc *ast.CommentGroup
	found := false
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == name {
				doc, found = d.Doc, true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				v, ok := s.(*ast.ValueSpec)
				if !ok || !slices.ContainsFunc(v.Names, func(n *ast.Ident) bool { return n.Name == name }) {
					continue
				}
				if doc, found = v.Doc, true; doc == nil {
					doc = d.Doc
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s declares no %s", file, name)
	}
	if text := strings.Join(strings.Fields(doc.Text()), " "); !strings.Contains(text, advice) {
		t.Errorf("Go doc of %s in %s = %q; want it to carry %q", name, file, text, advice)
	}
}

// adviceAssertManifest fails unless verb's manifest text (its Description
// when param is "", else that parameter's), which help and MCP tools/list
// show, carries every phrase word for word.
func adviceAssertManifest(t *testing.T, verb, param string, phrases ...string) {
	t.Helper()
	v, ok := manifest.Lookup(verb)
	if !ok {
		t.Fatalf("no %s verb in the manifest", verb)
	}
	text, found := v.Description, param == ""
	for _, p := range v.Params {
		if param != "" && p.Name == param {
			text, found = p.Description, true
		}
	}
	if !found {
		t.Fatalf("manifest has no %s parameter %q", verb, param)
	}
	for _, p := range phrases {
		if !strings.Contains(text, p) {
			t.Errorf("%s %q text %q\nwant it to carry %q", verb, param, text, p)
		}
	}
}

// adviceAwaitFinished follows "do not retry until get shows the row ended or
// missing" and the pending-row texts that point to find-missing: get shows id
// pending with a launch start; with inside set, find-missing 1 s before the
// pending grace period ends leaves it pending and inside runs; 1 s after it
// ends find-missing runs and get must show the row ended or missing, which it
// returns. The clock only moves forward.
func adviceAwaitFinished(t *testing.T, c *api.Client, clock *tmuxfix.Clock, id string, inside func()) string {
	t.Helper()
	row, err := c.Get(id)
	if err != nil || row.State != store.StatePending || row.LaunchStartedAt == nil {
		t.Fatalf("Get(%s) = state %q, launch start %v, %v; want pending with a launch start", id, row.State, row.LaunchStartedAt, err)
	}
	start := *row.LaunchStartedAt
	sweep := func(at time.Time) string {
		t.Helper()
		if d := at.Sub(clock.Now()); d > 0 {
			clock.Advance(d)
		}
		if _, err := c.FindMissing(context.Background()); err != nil {
			t.Fatalf("FindMissing: %v", err)
		}
		row, err := c.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after find-missing: %v", id, err)
		}
		return row.State
	}
	if inside != nil {
		if st := sweep(start.Add(fmGrace - time.Second)); st != store.StatePending {
			t.Fatalf("Get(%s) after find-missing inside the pending grace period = %q; want pending", id, st)
		}
		inside()
	}
	st := sweep(start.Add(fmGrace + time.Second))
	if st != store.StateEnded && st != store.StateMissing {
		t.Fatalf("Get(%s) after find-missing past grace = %q; want ended or missing", id, st)
	}
	return st
}

// adviceHeldFollow is a "duplicate session" whose refusal B10 and A12 follow: the re-lookup's arrangement, the words
// of its refusal, whether only a restore that leaves the row pending is tried, and whether the holder outlives the wait.
type adviceHeldFollow struct {
	name, words string
	spec        heldSpec
	pendingOnly bool
	keeps       bool
}

// adviceHeldFollows: the re-lookup times out; or the row's own young session appears to still be starting (b.gu6),
// and goes during the wait or keeps running.
func adviceHeldFollows() []adviceHeldFollow {
	starting := heldSpec{Holder: holderCurrent}
	return []adviceHeldFollow{
		{"", "no answer within", heldSpec{Holder: holderNone, Relookup: tmuxfix.Script{Failure: tmux.FailTimeout}}, false, false},
		{"still starting, the session goes/", "appears to still be starting", starting, true, false},
		{"still starting, the session keeps running/", "appears to still be starting", starting, true, true},
	}
}

// adviceOnceAfter runs fn once, when the next call of kind call on rec returns.
func adviceOnceAfter(rec *tmuxfix.Recorder, call tmux.Call, fn func()) {
	done := false
	rec.AfterCall(call, func(tmuxfix.SocketCall, error) {
		if !done {
			done = true
			fn()
		}
	})
}

// adviceEndSession ends session id on socket, as its own exit or a human would.
func adviceEndSession(t *testing.T, rec *tmuxfix.Recorder, socket, id string) {
	t.Helper()
	if err := rec.KillSessionID(socket, id); err != nil {
		t.Fatalf("KillSessionID(%s, %s): %v", socket, id, err)
	}
}

// adviceOtherRow seeds another finished row, its agent gone and no session,
// and returns its id (a parent id to write to a row).
func adviceOtherRow(t *testing.T, e *killEnv) string {
	t.Helper()
	return e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
}

// adviceReuseSettled seeds a reusable row (no relatives) whose agent is gone
// and which ended long ago, so a lookup decides it alone.
func adviceReuseSettled(t *testing.T, e *killEnv) reuseRow {
	t.Helper()
	return e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Bare: true})
}
