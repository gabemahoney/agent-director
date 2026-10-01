package api_test

// sendkeys_pending_test.go: send-keys --allow-pending on a pending launch of
// a fresh spawn and of a resumed row (SR-7.1, SR-7.2, SR-3.4, SR-3.6, SR-22.7,
// SR-22.8; AC-PANE-09, AC-SPN-07, AC-LKP-20): delivered only into the current
// launch's own pane by id, the refusals with nothing sent, the no-launch-identity
// refusals with no tmux call, and a row change between the read and the send.

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// skpText is the text every case sends.
const skpText = "first prompt"

// skpSend runs send-keys with allow_pending on id.
func skpSend(e *killEnv, id string) error {
	_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: id, Text: skpText, AllowPending: true})
	return err
}

// skpClockSteps are the fixture clock steps made between seeding and the call.
var skpClockSteps = []struct {
	name string
	d    time.Duration
}{{"clock as seeded", 0}, {"clock stepped forward", 48 * time.Hour}, {"clock stepped back", -48 * time.Hour}}

// skpSeedNoSession seeds k's pending row with shape v and no session.
func skpSeedNoSession(t *testing.T, e *killEnv, k pendingKind, v pendingShape) killRow {
	t.Helper()
	spec := e.pendingSpec(k, v)
	spec.NoSession = true
	return e.seedRow(t, spec)
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

// skpAdopted counts id's send-keys ad.provenance.disagree records with reason adopted.
func skpAdopted(t *testing.T, id string) int {
	t.Helper()
	n := 0
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == "send-keys" && l["reason"] == "adopted" {
			n++
		}
	}
	return n
}

// skpAssertAdoptedOnce fails unless after is before with the server identity
// and pane (nil: none) written in one write.
func skpAssertAdoptedOnce(t *testing.T, before, after adoptionColumns, pane any) {
	t.Helper()
	if after.RowVersion != before.RowVersion+1 || after.ServerPID == nil || after.PaneID != pane {
		t.Errorf("adoption %+v after %+v; want row_version +1, the server identity and pane %v", after, before, pane)
	}
}

// TestSendKeysPendingDelivered: a pending launch's own pane gets the text and
// Enter by id, whatever the clock did; a lost reply's adoption is written once.
func TestSendKeysPendingDelivered(t *testing.T) {
	cases := []struct {
		name string
		seed func(*testing.T, *killEnv, pendingKind) killRow
		// lostReply: the pane is the token pane, and the adoption is written once.
		lostReply bool
	}{
		{name: "Ours", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			return e.seedPending(t, k, pendingOurs)
		}},
		{name: "Ours renamed", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			r := skpSeedNoSession(t, e, k, pendingOurs)
			e.seedSession(t, &r, tmuxfix.WithRowSessionName("renamed-"+uuid.NewString()[:8]))
			return r
		}},
		{name: "lost reply", lostReply: true, seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			return e.seedPending(t, k, pendingLostReply)
		}},
		{name: "an earlier launch's session still running beside", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			r := e.seedPending(t, k, pendingOurs)
			e.seedLeftover(t, r, tmuxfix.OtherToken)
			return r
		}},
		{name: "an earlier launch's session beside a lost reply", lostReply: true,
			seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
				r := e.seedPending(t, k, pendingLostReply)
				e.seedLeftover(t, r, tmuxfix.OtherToken)
				return r
			}},
		{name: "another store's session with this row's token beside", seed: func(t *testing.T, e *killEnv, k pendingKind) killRow {
			r := e.seedPending(t, k, pendingOurs)
			skpOtherStore(t, e, r)
			return r
		}},
	}
	for _, k := range pendingKinds() {
		for _, tc := range cases {
			for _, step := range skpClockSteps {
				t.Run(k.String()+"/"+tc.name+"/"+step.name, func(t *testing.T) {
					e := newKillEnv(t)
					r := tc.seed(t, e, k)
					pane := r.Spawn.Identity.PaneID
					if tc.lostReply {
						pane = labelledPane(t, r.Session, r.Token)
					}
					cols, before := e.columns(t, r.ID), e.adoption(t, r.ID)
					e.clock.Advance(step.d)

					if err := skpSend(e, r.ID); err != nil {
						t.Fatalf("SendKeys: %v", err)
					}
					e.assertDelivered(t, r.Socket, pane, skpText)
					if !tc.lostReply {
						e.assertRowUnchanged(t, r.ID, cols)
						return
					}
					after := e.adoption(t, r.ID)
					skpAssertAdoptedOnce(t, before, after, pane)
					if n := skpAdopted(t, r.ID); n != 1 {
						t.Errorf("adopted records = %d; want 1", n)
					}

					e.rec.Reset()
					if err := skpSend(e, r.ID); err != nil {
						t.Fatalf("second SendKeys: %v", err)
					}
					e.assertDelivered(t, r.Socket, pane, skpText)
					if again := e.adoption(t, r.ID); again != after {
						t.Errorf("adoption after the second call = %+v; want no further write %+v", again, after)
					}
					if n := skpAdopted(t, r.ID); n != 1 {
						t.Errorf("adopted records after the second call = %d; want still 1", n)
					}
				})
			}
		}
	}
}

// skpRefusal is a refused pending case's expectation: the one error name,
// its description and the calls made (never a text or Enter).
type skpRefusal struct {
	name  string
	desc  apitest.DescCase
	calls []tmux.Call
}

// TestSendKeysPendingRefused: leftover, different server, conflicting labels, an
// unadoptable lost reply and another store's sessions send nothing, whatever the clock did.
func TestSendKeysPendingRefused(t *testing.T) {
	lookup := []tmux.Call{tmux.CallLookup}
	listed := []tmux.Call{tmux.CallLookup, tmux.CallListPanes}
	leftover := func(_ *killEnv, r killRow) skpRefusal {
		return skpRefusal{"ErrSpawnNotInteractive", apitest.DescSendKeysPendingLeftover(r.ID,
			[]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}), lookup}
	}
	gone := func(_ *killEnv, r killRow) skpRefusal {
		return skpRefusal{"ErrTmuxSendKeys", apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneSendKeys,
			InstanceID: r.ID, Name: r.Name}), lookup}
	}
	notAdopted := func(_ *killEnv, r killRow) skpRefusal {
		return skpRefusal{"ErrTmuxSessionConflict", apitest.DescPaneNotFound(apitest.PaneNotFound{
			Verb: apitest.PaneSendKeys, InstanceID: r.ID, Name: r.Session.Name, LostReply: true}), listed}
	}
	cases := []struct {
		name string
		// seed seeds the row and its world; forbid is text of it no description may carry.
		seed func(*testing.T, *killEnv, pendingKind) (r killRow, forbid []string)
		want func(*killEnv, killRow) skpRefusal
		// adoptsServer: a lost reply whose server identity is written once, no pane.
		adoptsServer bool
	}{
		{name: "Leftover", want: leftover, seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
			return e.seedPending(t, k, pendingLeftover), nil
		}},
		{name: "different server", seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
			r := e.seedPending(t, k, pendingOurs)
			ktrRebind(t, e, &r)
			return r, nil
		}, want: func(_ *killEnv, r killRow) skpRefusal {
			return skpRefusal{"ErrTmuxNotAvailable", apitest.DescDifferentServer(r.ID), lookup}
		}},
		{name: "two sessions with the current label", seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
			r := e.seedPending(t, k, pendingOurs)
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + uuid.NewString()[:8], Label: r.current()})
			return r, nil
		}, want: func(e *killEnv, r killRow) skpRefusal {
			var carrying []apitest.DescSession
			for _, s := range e.rec.Sessions(r.Socket) {
				if s.Label == r.current() {
					carrying = append(carrying, apitest.DescSession{Name: s.Name, ID: s.ID})
				}
			}
			return skpRefusal{"ErrTmuxSessionConflict", apitest.DescConflictingLabels(apitest.ConflictingLabels{
				InstanceID: r.ID, Sessions: carrying, NothingWasDone: true}), lookup}
		}},
		{name: "lost reply, no pane carries the token", want: notAdopted, adoptsServer: true,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := skpSeedNoSession(t, e, k, pendingLostReply)
				e.seedOurs(t, &r, tmuxfix.SeedPane{PID: e.newPID()})
				return r, nil
			}},
		{name: "lost reply, two panes carry the token", want: notAdopted, adoptsServer: true,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := skpSeedNoSession(t, e, k, pendingLostReply)
				e.seedOurs(t, &r, tmuxfix.SeedPane{PID: e.newPID(), AdPane: r.Token},
					tmuxfix.SeedPane{Index: 1, PID: e.newPID(), AdPane: r.Token})
				return r, nil
			}},
		{name: "only another store's session, with this row's token", want: gone,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := skpSeedNoSession(t, e, k, pendingOurs)
				e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.otherStore(r.Token), true))
				return r, nil
			}},
		{name: "only another store's session, with another token", want: gone,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := skpSeedNoSession(t, e, k, pendingOurs)
				e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.otherStore(newToken()), true))
				return r, nil
			}},
		{name: "Leftover beside another store's session", want: leftover,
			seed: func(t *testing.T, e *killEnv, k pendingKind) (killRow, []string) {
				r := e.seedPending(t, k, pendingLeftover)
				return r, []string{skpOtherStore(t, e, r).Name}
			}},
	}
	for _, k := range pendingKinds() {
		for _, tc := range cases {
			for _, step := range skpClockSteps {
				t.Run(k.String()+"/"+tc.name+"/"+step.name, func(t *testing.T) {
					e := newKillEnv(t)
					r, forbid := tc.seed(t, e, k)
					want := tc.want(e, r)
					cols, before := e.columns(t, r.ID), e.adoption(t, r.ID)
					e.clock.Advance(step.d)

					err := skpSend(e, r.ID)

					assertOneName(t, err, want.name)
					if err == nil {
						return
					}
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
}

// TestSendKeysPendingNoLaunchIdentity: no usable launch start or token, under any
// recorded name, is refused before the lookup with no tmux call.
func TestSendKeysPendingNoLaunchIdentity(t *testing.T) {
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
	for _, k := range pendingKinds() {
		for _, id := range identities {
			for _, n := range names {
				t.Run(k.String()+"/"+id.name+"/"+n.name, func(t *testing.T) {
					e := newKillEnv(t)
					spec := e.pendingSpec(k, pendingOurs, append([]apitest.SpawnOption{id.opt}, n.opts...)...)
					spec.NoSession = true
					r := e.seedRow(t, spec)
					cols := e.columns(t, r.ID)

					err := skpSend(e, r.ID)

					assertOneName(t, err, "ErrSpawnNotInteractive")
					if err != nil {
						apitest.AssertDescription(t, err.Error(), apitest.DescSendKeysPendingNoLaunch(r.ID))
					}
					e.assertNoTmuxCall(t)
					e.assertRowUnchanged(t, r.ID, cols)
				})
			}
		}
	}
}

// TestSendKeysPendingRowChangesBeforeSend: a gated SessionStart, or a versioned write on a lost
// reply (adoption then not applied), after the lookup or listing still sends to the same pane.
func TestSendKeysPendingRowChangesBeforeSend(t *testing.T) {
	for _, k := range pendingKinds() {
		for _, call := range []tmux.Call{tmux.CallLookup, tmux.CallListPanes} {
			t.Run(k.String()+"/SessionStart after "+string(call), func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedPending(t, k, pendingOurs)
				e.sessionStartAfter(t, call, r, "sess-start-"+uuid.NewString()[:8])

				if err := skpSend(e, r.ID); err != nil {
					t.Fatalf("SendKeys: %v", err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
				if got := e.columns(t, r.ID).State; got == store.StatePending {
					t.Errorf("state after the SessionStart = %v; want the row turned live", got)
				}
			})
			t.Run(k.String()+"/lost reply, row write after "+string(call), func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedPending(t, k, pendingLostReply)
				pane := labelledPane(t, r.Session, r.Token)
				before := e.adoption(t, r.ID)
				e.rowWriteAfter(t, call, r)

				if err := skpSend(e, r.ID); err != nil {
					t.Fatalf("SendKeys: %v", err)
				}
				e.assertDelivered(t, r.Socket, pane, skpText)
				want := before
				want.RowVersion++
				if after := e.adoption(t, r.ID); after != want {
					t.Errorf("adoption = %+v; want only the row write's row_version +1 %+v", after, want)
				}
				if n := skpAdopted(t, r.ID); n != 0 {
					t.Errorf("adopted records = %d; want none (the write did not apply)", n)
				}
			})
		}
	}
}
