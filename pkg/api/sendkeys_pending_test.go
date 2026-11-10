package api_test

// sendkeys_pending_test.go: send-keys with allow_pending on a pending launch of
// a fresh spawn and a reuse (SR-7.1, SR-7.2, SR-3.4, SR-3.6, SR-22.7, SR-22.8,
// SR-10; AC-PANE-08, AC-PANE-09, AC-SPN-07, AC-LKP-20): delivered only into
// the current launch's own pane by id, the refusals with nothing sent, the
// no-launch-identity refusals with no tmux call, a row change between the read
// and the send, and a reuse's read-pane and allow_pending halves. send-keys
// now waits on raw mode, SessionStart and its settle for pending Claude (b.qcf).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPaneOnlyLifecycleWithFlagOff(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			e := newKillEnv(t)
			e.rowAgent = agent
			r := e.seedPending(t, pendingFresh, pendingOurs)
			c, _ := e.client(t) // experimental flags remain at their disabled defaults
			e.clock.Advance(24 * time.Hour)
			res, err := c.FindMissing(context.Background())
			if err != nil || res.Count != 0 {
				t.Fatalf("elapsed time reconciled a living lane: %+v, %v", res, err)
			}
			if row := e.columns(t, r.ID); row.State != store.StatePending {
				t.Fatalf("live row=%+v", row)
			}
			e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "native prompt")
			pane, err := c.ReadPane(api.ReadPaneParams{InstanceID: r.ID})
			if err != nil || pane.Pane != "native prompt" {
				t.Fatalf("ReadPane=%+v, %v", pane, err)
			}
			e.rec.AfterCall(tmux.CallKillPane, func(tmuxfix.SocketCall, error) { e.pc.Set(r.AgentPID, procfix.Gone()) })
			k, err := c.Kill(api.KillParams{InstanceID: r.ID})
			if err != nil || !k.KillSent {
				t.Fatalf("Kill=%+v, %v", k, err)
			}
			res, err = c.FindMissing(context.Background())
			if err != nil || res.Count != 1 || res.IDs[0] != r.ID {
				t.Fatalf("exited row reconciliation=%+v, %v", res, err)
			}
			e.clock.Advance(time.Minute)
			expired, err := c.Expire(olderThan(0))
			if err != nil || expired.Count != 1 || expired.IDs[0] != r.ID {
				t.Fatalf("flag-off Expire=%+v, %v", expired, err)
			}
		})
	}
}

func TestPaneOnlyPlainInputBypassesNativeDelivery(t *testing.T) {
	// Serial: isolate native lock/evidence paths under a temporary HOME.
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			home := withTempHome(t)
			e := newNativeSubmissionEnv(t)
			e.rowAgent = agent
			r := e.seedPending(t, pendingFresh, pendingOurs)
			// The injected backend supports native delivery; even Open followed
			// by a silent fallback must be visible after the call.
			in := watchPaneInput(e.killEnv, r.Spawn.Identity.PaneID, false)
			before := e.columns(t, r.ID)
			result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: "plain input", AllowPending: true})
			if err != nil || result != (api.SendKeysResult{}) {
				t.Fatalf("plain lane send=%+v, %v; want empty manual result", result, err)
			}
			in.assertSubmittedOnce(t, "plain input")
			if enters := ordinaryEnters(e.killEnv); len(enters) != 1 || enters[0].Socket != r.Socket || enters[0].Target != r.Spawn.Identity.PaneID {
				t.Fatalf("manual Enter calls=%+v; want one to the owned pane", enters)
			}
			e.assertNoCalls(t, tmux.CallCapture, tmux.CallPaneTerminal)
			e.assertRowUnchanged(t, r.ID, before)
			if e.b.opened != 0 || e.b.locked || e.b.current != nil || len(e.b.history) != 0 || e.slept != 0 {
				t.Fatalf("native activity: opens=%d locked=%t current=%+v history=%+v wait=%v",
					e.b.opened, e.b.locked, e.b.current, e.b.history, e.slept)
			}
			if launch, err := e.st.ReadInputLaunch(context.Background(), r.Token); err != nil || launch != nil {
				t.Fatalf("native launch journal=%+v, %v; want absent", launch, err)
			}
			if files := snapshotTrees(t, []string{filepath.Join(adConfigDir(home), "input")}); len(files) != 0 {
				t.Fatalf("manual send created native lock/evidence files: %v", files)
			}
		})
	}
}

func TestPaneOnlyManualInput(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"droid"} {
		for _, mode := range []string{"enter", "no enter", "empty", "without opt-in", "changed hash", "failed text", "failed enter"} {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				e := newKillEnv(t)
				e.rowAgent = agent
				r := e.seedPending(t, pendingFresh, pendingOurs)
				in := watchPaneInput(e, r.Spawn.Identity.PaneID, true)
				e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "ready")
				pane, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
				if err != nil || pane.Pane != "ready" {
					t.Fatalf("ReadPane=%+v, %v", pane, err)
				}
				e.rec.Reset()
				p := api.SendKeysParams{InstanceID: r.ID, Text: "first\r\nsecond", AllowPending: true, ExpectPaneSHA256: pane.PaneSHA256}
				wantError := ""
				switch mode {
				case "no enter":
					p.NoEnter = true
				case "empty":
					p.Text = ""
				case "without opt-in":
					p.AllowPending = false
					wantError = "ErrSpawnNotInteractive"
				case "changed hash":
					e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "changed")
					wantError = "ErrPaneChanged"
				case "failed text":
					e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallSendText)
					wantError = "ErrTmuxUnresponsive"
				case "failed enter":
					e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, callEnterKey)
					wantError = "ErrTmuxUnresponsive"
				}
				before := e.columns(t, r.ID)
				res, err := e.sendKeys(p)
				if wantError != "" {
					assertOneName(t, err, wantError)
				} else if err != nil {
					t.Fatal(err)
				}
				if res != (api.SendKeysResult{}) {
					t.Fatalf("manual input returned proof: %+v", res)
				}
				e.assertRowUnchanged(t, r.ID, before)
				texts, enters := e.rec.SocketCallsOf(tmux.CallSendText), e.rec.SocketCallsOf(callEnterKey)
				if mode == "without opt-in" || mode == "changed hash" {
					e.assertNothingSent(t)
					return
				}
				wantTexts, wantEnters := 1, 1
				if mode == "empty" {
					wantTexts = 0
				}
				if mode == "no enter" || mode == "failed text" {
					wantEnters = 0
				}
				if len(texts) != wantTexts || len(enters) != wantEnters {
					t.Fatalf("text=%+v Enter=%+v; want %d/%d, no replay", texts, enters, wantTexts, wantEnters)
				}
				if wantTexts == 1 && (texts[0].Text != "first\nsecond" || texts[0].Target != r.Spawn.Identity.PaneID) {
					t.Fatalf("manual text=%+v", texts)
				}
				if mode == "enter" {
					in.assertSubmittedOnce(t, "first\nsecond")
				}
				if mode == "no enter" && (in.box != "first\nsecond" || len(in.submitted) != 0) {
					t.Fatalf("unsubmitted input=%+v", in)
				}
			})
		}
	}
}

func TestPaneOnlyUnsupportedVerbsWriteNothing(t *testing.T) {
	// Serial: it checks the shared trail for writes.
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			p := newPAEnvAgent(t, agent, "")
			c, _ := p.client(t)
			for _, operation := range []struct {
				name, want string
				run        func() error
			}{
				{"pause", "ErrAgentCapabilityUnsupported", func() error { _, err := c.Pause(context.Background(), api.PauseParams{InstanceID: p.r.ID}); return err }},
				{"resume", "ErrAgentCapabilityUnsupported", func() error { _, err := c.Resume(api.ResumeParams{InstanceID: p.r.ID}); return err }},
				{"decide", "ErrAgentCapabilityUnsupported", func() error {
					_, err := c.Decide(api.DecideParams{InstanceID: p.r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "deny"})
					return err
				}},
				{"record-pane-answer", "ErrAgentCapabilityUnsupported", func() error { _, err := c.RecordPaneAnswer(p.outside(storefix.TestRequestTokenA, "deny")); return err }},
				{"delivery ID", "ErrAgentCapabilityUnsupported", func() error {
					_, err := c.SendKeys(api.SendKeysParams{InstanceID: p.r.ID, Text: "task", DeliveryRequestID: "attempt", AllowPending: true})
					return err
				}},
				{"request token", "ErrNoOpenPermissionRequest", func() error { _, err := c.SendKeys(p.answer(storefix.TestRequestTokenA, "deny", "Escape")); return err }},
				{"unknown request", "ErrPermissionRequestNotFound", func() error { _, err := c.RecordPaneAnswer(p.outside("unknown", "deny")); return err }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					before := p.snapshotWrites(t, operation.name, p.r.ID, trustConfig{}, p.r.Socket)
					assertOneName(t, operation.run(), operation.want)
					if operation.name == "delivery ID" || operation.name == "request token" {
						events := readAPITrailLines(t)[before.mark:]
						if len(events) != 1 || events[0]["event"] != "ad.send_keys.called" || events[0]["instance_id"] != p.r.ID {
							t.Fatalf("refused send-keys trail=%+v; want only its observational called event", events)
						}
						before.mark = trailMark(t)
					}
					p.assertWroteNothing(t, before)
					p.assertNoTmuxCall(t)
				})
			}
		})
	}
}

// skpText is the text every case sends.
const skpText = "first prompt"

// skpKinds are the pending launches the cases run on.
var skpKinds = []pendingKind{pendingFresh, pendingReused}

// skpSend runs send-keys with allow_pending on id.
func skpSend(e *killEnv, id string) error {
	_, err := e.sendKeys(api.SendKeysParams{InstanceID: id, Text: skpText, AllowPending: true})
	return err
}

// skpOtherStore seeds, under a new name, another store's session labelled with
// r's id and token whose one pane carries r's token.
func skpOtherStore(t *testing.T, e *killEnv, r killRow) tmuxfix.SeedSession {
	t.Helper()
	e.ensureServer(&r)
	s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "other-store-" + uuid.NewString()[:8],
		Label: r.otherStore(r.Token), Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}})
	e.syncServers()
	return s
}

// skpAssertAdoptedOnce fails unless after is before with the server identity
// and pane (nil: none) written in one write.
func skpAssertAdoptedOnce(t *testing.T, before, after adoptionColumns, pane any) {
	t.Helper()
	if after.RowVersion != before.RowVersion+1 || after.ServerPID == nil || after.PaneID != pane {
		t.Errorf("adoption %+v after %+v; want row_version +1, the server identity and pane %v", after, before, pane)
	}
}

// b.qcf: delivery without a turn hook is unconfirmed; adoption and targeting stay unchanged.
// TestSendKeysPendingDelivered: a pending launch of a fresh spawn or a reuse
// gets the text and Enter in its own pane by id; a lost reply's adoption is
// written once, and a second send writes nothing more. For a reuse
// (AC-PANE-08), read-pane reads that pane with the row still pending.
func TestSendKeysPendingDelivered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		seed      func(*testing.T, *killEnv, pendingKind) killRow
		lostReply bool // the pane is the token pane, and the adoption is written once
	}{
		{name: "Ours", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow { return e.seedPending(t, k, pendingOurs) }},
		{name: "lost reply", lostReply: true, seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			return e.seedPending(t, k, pendingLostReply)
		}},
		{name: "an earlier launch's session still running beside", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			r := e.seedPending(t, k, pendingOurs)
			e.seedLeftover(t, r, tmuxfix.OtherToken)
			return r
		}},
		{name: "another store's session with this row's token beside", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			r := e.seedPending(t, k, pendingOurs)
			skpOtherStore(t, e, r)
			return r
		}},
	}
	pane := func(t *testing.T, r killRow, lostReply bool) string {
		if lostReply {
			return labelledPane(t, r.Session, r.Token)
		}
		return r.Spawn.Identity.PaneID
	}
	for _, k := range skpKinds {
		for _, tc := range cases {
			if k == pendingReused {
				t.Run(k.String()+"/"+tc.name+"/read-pane", func(t *testing.T) {
					t.Parallel()
					e := newKillEnv(t)
					r := tc.seed(t, e, k)
					e.setPaneTexts(r.Socket)
					want := paneText(r.Socket, pane(t, r, tc.lostReply))
					if res, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID}); err != nil || res.Pane != want {
						t.Errorf("ReadPane = %q, %v; want %q", res.Pane, err, want)
					}
					if got := e.columns(t, r.ID).State; got != store.StatePending {
						t.Errorf("state after read-pane = %v; want pending", got)
					}
				})
			}
			t.Run(k.String()+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := tc.seed(t, e, k)
				pane := pane(t, r, tc.lostReply)
				cols, before := e.columns(t, r.ID), e.adoption(t, r.ID)
				e.sessionStartsWhenPolled(t, r, func() { cols = e.columns(t, r.ID) })

				assertSubmissionUnconfirmed(t, skpSend(e, r.ID), store.StateWaiting)
				e.assertDelivered(t, r.Socket, pane, skpText, 2)
				if !tc.lostReply {
					e.assertRowUnchanged(t, r.ID, cols)
					return
				}
				after := e.adoption(t, r.ID)
				// b.qcf: SessionStart adds one versioned write after adoption.
				adopted := after
				adopted.RowVersion--
				skpAssertAdoptedOnce(t, before, adopted, pane)

				e.rec.Reset()
				if err := skpSend(e, r.ID); err != nil {
					t.Fatal(err)
				} // Now waiting: the established route.
				e.assertDelivered(t, r.Socket, pane, skpText)
				e.assertRowUnchanged(t, r.ID, cols)
				if again := e.adoption(t, r.ID); again != after {
					t.Errorf("adoption after the second call = %+v; want no further write %+v", again, after)
				}
				if n := adoptedRecords(t, "send-keys", r.ID); n != 1 {
					t.Errorf("adopted records after two calls = %d; want 1", n)
				}
			})
		}
	}
}

// TestSendKeysPendingRefused: on a fresh spawn's or a reuse's pending launch,
// Leftover (another store's session beside it too) and an unadoptable lost
// reply send nothing. The other lookup refusals on a pending row are the call
// table's send-keys pending row.
func TestSendKeysPendingRefused(t *testing.T) {
	t.Parallel()
	type refusal struct {
		name  string
		desc  apitest.DescCase
		calls []tmux.Call
	}
	leftover := func(_ *killEnv, r killRow) refusal {
		return refusal{"ErrSpawnNotInteractive", apitest.DescSendKeysPendingLeftover(r.ID,
			[]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}), paneLookupCalls}
	}
	notAdopted := func(_ *killEnv, r killRow) refusal {
		return refusal{"ErrTmuxSessionConflict", apitest.DescPaneNotFound(apitest.PaneNotFound{
			Verb: apitest.PaneSendKeys, InstanceID: r.ID, Name: r.Session.Name, LostReply: true}), paneListedCalls}
	}
	cases := []struct {
		name string
		// seed seeds the row and its world; forbid is text of it no description may carry.
		seed func(*testing.T, *killEnv, pendingKind) (r killRow, forbid []string)
		want func(*killEnv, killRow) refusal
		// adoptsServer: a lost reply whose server identity is written once, no pane.
		adoptsServer bool
	}{
		{name: "Leftover", want: leftover, seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
			return e.seedPending(t, k, pendingLeftover), nil
		}},
		{name: "lost reply, no pane carries the token", want: notAdopted, adoptsServer: true,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := e.seedPendingNoSession(t, k, pendingLostReply)
				e.seedOurs(t, &r, e.otherPane())
				return r, nil
			}},
		{name: "lost reply, two panes carry the token", want: notAdopted, adoptsServer: true,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := e.seedPendingNoSession(t, k, pendingLostReply)
				e.seedOurs(t, &r, tmuxfix.SeedPane{PID: e.newPID(), AdPane: r.Token},
					tmuxfix.SeedPane{Index: 1, PID: e.newPID(), AdPane: r.Token})
				return r, nil
			}},
		{name: "Leftover beside another store's session", want: leftover,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := e.seedPending(t, k, pendingLeftover)
				return r, []string{skpOtherStore(t, e, r).Name}
			}},
	}
	for _, k := range skpKinds {
		for _, tc := range cases {
			t.Run(k.String()+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r, forbid := tc.seed(t, e, k)
				want := tc.want(e, r)
				cols, before := e.columns(t, r.ID), e.adoption(t, r.ID)

				err := skpSend(e, r.ID)

				assertOneName(t, err, want.name)
				apitest.AssertDescription(t, err.Error(), want.desc,
					append(forbid, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))...)
				e.assertPaneCalls(t, want.calls...)
				e.assertNothingSent(t)
				if tc.adoptsServer {
					skpAssertAdoptedOnce(t, before, e.adoption(t, r.ID), nil)
				} else {
					e.assertRowUnchanged(t, r.ID, cols)
				}
			})
		}
	}
}

// TestSendKeysPendingNoLaunchIdentity: no usable launch start or token, under any
// recorded name, is refused before the lookup with no tmux call (a reuse records both).
func TestSendKeysPendingNoLaunchIdentity(t *testing.T) {
	t.Parallel()
	identities := []struct {
		name string
		opt  apitest.SpawnOption
	}{
		{"no launch start", apitest.WithNoLaunchStartedAt()},
		{"unreadable launch start, text", apitest.WithRawLaunchStartedAt("2026-09-30T12:00:00Z")},
		{"unreadable launch start, real", apitest.WithRawLaunchStartedAt(1.5)},
		{"launch start out of range", apitest.WithLaunchStartedAt(253402300800000)},
		{"no launch token", apitest.WithNoLaunchToken()},
		{"malformed launch token", apitest.WithLaunchIdentity(store.LaunchIdentity{Token: "NOT-A-TOKEN", Socket: apitest.TestSocket})},
	}
	names := []struct {
		name string
		opts []apitest.SpawnOption
	}{
		{"recorded name", nil},
		{"empty name", []apitest.SpawnOption{apitest.WithTmuxSessionName("")}},
		{"control character in the name", []apitest.SpawnOption{apitest.WithTmuxSessionName("a\tb")}},
		{"a name tmux rewrites", []apitest.SpawnOption{apitest.WithTmuxSessionName("a.b:c\xff")}},
	}
	for _, id := range identities {
		for _, n := range names {
			t.Run(id.name+"/"+n.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				spec := e.pendingSpec(pendingOurs, append([]apitest.SpawnOption{id.opt}, n.opts...)...)
				spec.NoSession = true
				r := e.seedRow(t, spec)
				cols := e.columns(t, r.ID)

				err := skpSend(e, r.ID)

				assertOneName(t, err, "ErrSpawnNotInteractive")
				apitest.AssertDescription(t, err.Error(), apitest.DescSendKeysPendingNoLaunch(r.ID))
				e.assertNoTmuxCall(t)
				e.assertRowUnchanged(t, r.ID, cols)
			})
		}
	}
}

// TestSendKeysPendingRowChangesBeforeSend: on a fresh spawn's or a reuse's
// pending launch, a gated SessionStart, or a versioned write on a lost reply
// (the adoption then not applied), between the row read and the send keeps
// the same pane and launch identity. R11B1 (t2.7tz.tb.ib) also covers the
// earlier empty journal reconciliation window, before target discovery or
// admission. b.qcf: ordinary fallback text sends after SessionStart, and
// SessionStart alone does not confirm submission; a row that never reports
// in refuses not_started with nothing typed. Native delivery is unchanged.
func TestSendKeysPendingRowChangesBeforeSend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		shape   pendingShape
		when    tmux.Call
		session bool
		native  bool
	}{
		{"SessionStart after the listing", pendingOurs, tmux.CallListPanes, true, false},
		{"lost reply, row write after the listing", pendingLostReply, tmux.CallListPanes, false, false},
		{"SessionStart before journal load", pendingOurs, beforeJournalLoad, true, false},
		{"liveness write before journal load", pendingOurs, beforeJournalLoad, false, false},
		{"native SessionStart before journal load", pendingOurs, beforeJournalLoad, true, true},
		{"native liveness write before journal load", pendingOurs, beforeJournalLoad, false, true},
	}
	for _, k := range skpKinds {
		for _, tc := range cases {
			t.Run(k.String()+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newNativeSubmissionEnv(t)
				e.env.Input = nil
				e.clock.Advance(time.Now().Sub(e.clock.Now()))
				r := e.seedPending(t, k, tc.shape)
				pane := labelledPane(t, r.Session, r.Token)
				before := e.adoption(t, r.ID)
				if launch, err := e.st.ReadInputLaunch(context.Background(), r.Token); err != nil || launch != nil {
					t.Fatalf("want empty native journal, launch=%+v error=%v", launch, err)
				}
				sessionID := uuid.NewString()
				var raw []byte
				if tc.native {
					launch := e.enableSQLiteSubmission(t, r, sessionID)
					raw = sqliteProofRecord(t, launch, skpText)
					e.rec.AfterCall(tmuxfix.KeyCall("Enter"), func(tmuxfix.SocketCall, error) {
						_, attempt := sqliteAttempt(t, e, r)
						if attempt == nil || attempt.Phase != "enter_attempted" || !attempt.EnterAttempted {
							t.Fatalf("Enter before committed intent: %+v", attempt)
						}
						if err := os.WriteFile(launch.Capability.Transcript, append(raw, '\n'), 0600); err != nil {
							t.Fatal(err)
						}
					})
				}
				if tc.session {
					e.sessionStartAfter(t, tc.when, r, sessionID)
				} else {
					e.rowWriteAfter(t, tc.when, r)
				}

				result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
				switch {
				case tc.native:
					if err != nil {
						t.Fatalf("SendKeys: %v; details=%+v; calls=%+v", err, api.ErrDetails(err), e.rec.SocketCalls())
					}
					assertOrdinaryWrites(t, e.killEnv, r, skpText)
				case tc.session:
					assertSubmissionUnconfirmed(t, err, store.StateWaiting)
					e.assertDelivered(t, r.Socket, pane, skpText, 2)
				default:
					e.assertSessionStartMissing(t, err)
				}
				if tc.native && (result.Outcome != "submitted" || result.SessionID != sessionID || result.RecordSHA256 != inputdelivery.Hash(raw)) {
					t.Fatalf("native result=%+v; want exact post-Enter proof", result)
				}
				row, err := e.st.GetSpawn(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if sqliteInputIdentity(row, e.storeID) != sqliteInputIdentity(r.Spawn, e.storeID) {
					t.Fatalf("launch identity changed: before=%+v after=%+v", r.Spawn, row)
				}
				if row.RowVersion <= r.Spawn.RowVersion {
					t.Fatal("real preparation write did not advance the captured row version")
				}
				if tc.session {
					if row.State == store.StatePending {
						t.Errorf("state after the SessionStart = %v; want the row turned live", row.State)
					}
				} else {
					want := before
					want.RowVersion++
					if after := e.adoption(t, r.ID); after != want {
						t.Errorf("adoption = %+v; want only the row write's row_version +1 %+v", after, want)
					}
					if n := adoptedRecords(t, "send-keys", r.ID); n != 0 {
						t.Errorf("adopted records = %d; want none", n)
					}
				}
				if launch, err := e.st.ReadInputLaunch(context.Background(), r.Token); err != nil || (launch != nil) != tc.native {
					t.Fatalf("plain send invented native authority: launch=%+v error=%v", launch, err)
				}
			})
		}
	}
}

// TestSendKeysResumedPendingClaudeAdmission (b.x9o t2.7tz.5c.fd, MI F1): a
// resumed Claude launch's first SessionStart may keep or rotate its saved
// session id; after the wait, admission accepts either, the text and one Enter
// are sent once and the hook check observes the turn, and the store archives a
// rotated id to session history. A changed launch token, an agent pid other
// than the adopted pane's, or cancellation at the admission read after the
// wait is not_started with nothing typed and the last row state read.
func TestSendKeysResumedPendingClaudeAdmission(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"same session id", "rotated session id", "launch token", "agent pid", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			e := newNativeSubmissionEnv(t)
			e.env.Input = nil // the SQLite journal's real admission; no native capability
			saved, cwd := "saved-"+uuid.NewString()[:8], t.TempDir()
			r := e.seedRow(t, killRowSpec{State: store.StateEnded, SessionID: saved, CWD: cwd, NoSession: true,
				Opts: []apitest.SpawnOption{apitest.WithJsonlPath(apitest.SeedJsonl(t, cwd, saved))}})
			e.resumeWritesOnPane(t, r)
			row, err := e.st.GetSpawn(r.ID)
			if err != nil || row.State != store.StatePending || row.SessionID != saved || row.PID != 0 || row.ProcStarttime != "" {
				t.Fatalf("resumed row = %+v, %v; want pending with the saved session id and no agent process", row, err)
			}
			r.Spawn, r.Token = row, row.Identity.Token
			e.seedSession(t, &r) // the resumed launch's session, labelled with its new token
			sessionID := saved
			if change != "same session id" {
				sessionID = "rotated-" + uuid.NewString()[:8]
			}
			e.sessionStartsWhenPolled(t, r, nil, sessionID)
			e.turnStartsAfterEnter(t, r)
			hookReads := 0
			e.s.read = func(row api.Spawn) (api.Spawn, error) { hookReads++; return row, nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.env.Context = ctx
			admission := false
			e.rec.AfterCall(tmux.CallListPanes, func(tmuxfix.SocketCall, error) {
				// The wait's final ownership listing follows its second terminal read.
				admission = admission || len(e.rec.SocketCallsOf(tmux.CallPaneTerminal)) == 2
			})
			e.s.reread = func(row api.Spawn) (api.Spawn, error) {
				if admission {
					admission = false
					switch change {
					case "launch token":
						row.Identity.Token = tmuxfix.OtherToken
					case "agent pid":
						row.PID = e.newPID()
					case "cancelled":
						cancel()
					}
				}
				return row, nil
			}

			result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})

			switch change {
			case "same session id", "rotated session id":
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("resumed send = %+v, %v; want fallback success", result, err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText, 2)
				if hookReads != 1 {
					t.Errorf("hook reads = %d; want the turn observed on the first", hookReads)
				}
				var want []store.SessionHistoryEntry
				if change == "rotated session id" {
					want = []store.SessionHistoryEntry{{SessionID: saved, JSONLPath: row.JSONLPath}}
				}
				got, err := e.st.ListSessionHistory(r.ID, row.LifeNumber)
				if err != nil || len(got) != len(want) || len(want) == 1 && (got[0].SessionID != want[0].SessionID || got[0].JSONLPath != want[0].JSONLPath) {
					t.Errorf("session history = %+v, %v; want %+v", got, err, want)
				}
			default:
				reason := "input preparation changed launch or authority"
				if change == "cancelled" {
					reason = "the call was cancelled or reached its deadline before input; nothing was typed, so the same send-keys can be retried later"
				}
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StateWaiting, Reason: reason}
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
				if got := api.ErrDetails(err); got != want {
					t.Fatalf("refusal = %+v; want %+v", got, want)
				}
				e.assertNothingSent(t)
				if hookReads != 0 {
					t.Errorf("hook reads = %d; want none", hookReads)
				}
			}
		})
	}
}

// TestSendKeysPendingClaudeManualIntentAdoptedIdentity (b.x9o, b.qcf): after
// pending Claude's wait, fallback records its manual intent under the adopted
// launch identity (a lost reply's token pane), not the row's stale one. An
// unconfirmed intent commit refuses not_started with nothing typed and the last
// row state the call read (waiting), not the pending row it began with.
func TestSendKeysPendingClaudeManualIntentAdoptedIdentity(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "unconfirmed commit"}[fail], func(t *testing.T) {
			t.Parallel()
			e := newNativeSubmissionEnv(t)
			r := e.seedPending(t, pendingFresh, pendingLostReply)
			pane := labelledPane(t, r.Session, r.Token)
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			e.b.current = &api.InputDeliveryAttempt{RequestID: "earlier", Phase: "not_started"}
			var intent *api.InputDeliveryAttempt
			e.b.save = func(a *api.InputDeliveryAttempt) error {
				intent = cloneAttempt(a)
				if fail {
					return inputdelivery.ErrUnconfirmed
				}
				return nil
			}
			e.sessionStartsWhenPolled(t, r, nil)

			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})

			row, rowErr := e.st.GetSpawn(r.ID)
			if rowErr != nil || r.Spawn.Identity.PaneID != "" || row.Identity.PaneID != pane {
				t.Fatalf("row %+v, %v; want the lost reply's token pane %s adopted", row.Identity, rowErr, pane)
			}
			if want := sqliteInputIdentity(row, e.storeID); intent == nil || intent.Phase != "manual_input" || !intent.ManualInterference || intent.Identity != want {
				t.Fatalf("manual intent = %+v; want manual_input under the adopted identity %+v", intent, want)
			}
			if fail {
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StateWaiting,
					Reason: "cannot persist manual input intent"}
				if got := api.ErrDetails(err); got != want {
					t.Fatalf("refusal = %+v; want %+v", got, want)
				}
				e.assertNothingSent(t)
				return
			}
			assertSubmissionUnconfirmed(t, err, store.StateWaiting) // no turn hook follows the Enter
			e.assertDelivered(t, r.Socket, pane, skpText, 2)
		})
	}
}

// b.qcf: a liveness note does not confirm submission.
// TestSendKeysNotedUnreportedRow (b.kdf, CSCB): a pending row find-missing noted unreported is still a pending row
// to send-keys: refused without allow_pending (ErrSpawnNotInteractive, no tmux call);
// with it, ordinary text waits for SessionStart then refuses without input (b.qcf).
// Neither call changes the row or its note.
func TestSendKeysNotedUnreportedRow(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "without allow_pending", true: "with allow_pending"}[allow], func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, e.pendingSpec(pendingOurs, apitest.WithLivenessNote("unreported"),
				apitest.WithLivenessUnverifiedSince("2026-10-09 10:00:00")))
			cols := e.columns(t, r.ID)

			_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: allow})

			if allow {
				e.assertSessionStartMissing(t, err)
			} else {
				assertOneName(t, err, "ErrSpawnNotInteractive")
				e.assertNoTmuxCall(t)
			}
			e.assertRowUnchanged(t, r.ID, cols)
		})
	}
}

// TestSendKeysPendingReuseWithoutAllowPending (AC-PANE-08): without allow_pending a reuse's pending row,
// its session up or its reply lost, is ErrSpawnNotInteractive with no tmux call and the row unchanged.
func TestSendKeysPendingReuseWithoutAllowPending(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		shape pendingShape
	}{{"Ours", pendingOurs}, {"lost reply", pendingLostReply}} {
		t.Run(pendingReused.String()+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedPending(t, pendingReused, tc.shape)
			cols := e.columns(t, r.ID)

			_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: skpText})

			assertOneName(t, err, "ErrSpawnNotInteractive")
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, cols)
		})
	}
}
