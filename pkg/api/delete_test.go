package api_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDeleteRows pins the admin delete (Epic 8 AC #3): each id gets its own
// result, a bogus id does not abort the batch, a live row goes like a finished
// one (no state guard), and no ids give an empty non-nil map.
func TestDeleteRows(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		ids  []string
		want map[string]string
	}{
		"live, bogus and ended": {[]string{"row-live", "absent", "row-ended"},
			map[string]string{"row-live": "ok", "absent": "ErrSpawnNotFound", "row-ended": "ok"}},
		"no ids": {[]string{}, map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := apitest.SeedDeleteFixture(t)
			res, err := api.DeleteRows(s, tc.ids)
			if err != nil || res.Results == nil || !reflect.DeepEqual(res.Results, tc.want) {
				t.Fatalf("DeleteRows = %#v, %v; want %v", res.Results, err, tc.want)
			}
			for id, outcome := range tc.want {
				if _, err := s.GetSpawn(id); outcome == "ok" && !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("%s: GetSpawn err = %v; want it deleted", id, err)
				}
			}
			if _, err := s.GetSpawn("absent"); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("absent id: GetSpawn err = %v; want no row created", err)
			}
		})
	}
}

// failingCoveredDelete is the store delete reaches, its covered deletion
// failing with errInjectedStore before it writes.
type failingCoveredDelete struct{ *store.Store }

func (failingCoveredDelete) DeleteBusySendCovered(string, api.RowSnapshot, store.BusySendExitEvidence) (api.CondResult, error) {
	return 0, errInjectedStore
}

// failingExitScope is the store delete reaches, its exit-scope read failing
// with errInjectedStore; its other reads and writes are the store's.
type failingExitScope struct{ *store.Store }

func (failingExitScope) ReadBusySendExitScope(string) (store.BusySendExitScope, error) {
	return store.BusySendExitScope{}, errInjectedStore
}

// TestDeleteRowsCovered (SR-6.3; sf-answer-2): in one batch through the
// administrator adapter, a covered row whose owned processes are not all
// checked gone (its agent still runs; an issuer of its ended Launch cannot
// be checked) is ErrSpawnNotDeletable with nothing changed, and the other ids
// go on: an absent id, an uncovered (Codex) live row deleted
// unconditionally, its agent still running, and a covered live row whose
// agent is gone, never swept, with an unusable recorded name, deleted with no
// tmux call, its Held Message Final not_started record_deleted and found
// after the deletion. Decision 15 (sf-answer-3): of covered rows whose
// current Launch records no agent identity, one that never issued and owns no
// process is deleted, while one whose intent persisted with no issuer, or
// whose recorded helper is checked gone, is ErrSpawnNotDeletable with nothing
// changed. A store failure in the covered deletion is ErrInternal for that
// id, with nothing changed.
func TestDeleteRowsCovered(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	uncovered, running := e.seedRow(t, killRowSpec{Opts: agentRowOpts("codex")}), e.seedRow(t, killRowSpec{})
	if cov := busyFacts(t, e.dbPath, uncovered.ID).Coverage; cov != store.BusySendNotCovered {
		t.Fatalf("Codex row coverage = %s; want not_covered", cov)
	}
	uncheckable := e.seedRow(t, killRowSpec{SessionID: uuid.NewString()})
	gone := e.seedRow(t, killRowSpec{Agent: agentGone, NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("ad:x")}})
	unidentified := func() killRow {
		return e.seedRow(t, killRowSpec{Agent: agentNotRecorded, NoSession: true, SessionID: uuid.NewString()})
	}
	neverIssued, intentOnly, helperGone := unidentified(), unidentified(), unidentified()
	covered := []killRow{running, uncheckable, gone, neverIssued, intentOnly, helperGone}
	for _, r := range covered {
		seedBusyCovered(t, e, r.ID)
		busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "kept", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
			DeliveryRequestID: "del-" + r.ID})
	}
	busyWake(t, e, uncheckable.ID)
	e.busyIssue(t, uncheckable, &lcHelper)
	e.pc.Set(lcHelper.PID, procfix.Unreadable())
	e.endBySessionEnd(t, uncheckable)
	busyWake(t, e, intentOnly.ID)
	e.busyIssue(t, intentOnly, nil)
	gonePID := store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 7102, StartTime: "7102"}
	busyWake(t, e, helperGone.ID)
	e.busyIssue(t, helperGone, &gonePID)
	e.pc.Set(gonePID.PID, procfix.Gone())
	type kept struct {
		facts apitest.BusySendFacts
		cols  apitest.SpawnColumns
	}
	keep := map[string]kept{}
	for _, r := range covered {
		keep[r.ID] = kept{busyFacts(t, e.dbPath, r.ID), e.columns(t, r.ID)}
	}

	if res, err := api.DeleteRowsWith(failingCoveredDelete{e.st}, e.pc, []string{gone.ID}); err != nil || res.Results[gone.ID] != "ErrInternal" {
		t.Errorf("delete with a failing store = %+v, %v; want ErrInternal", res.Results, err)
	}
	// A failing exit-scope read is the store's failure too, although the
	// plain deletion's own binding read would succeed and refuse the covered
	// row (repair sf2o-2 CR4).
	if res, err := api.DeleteRowsWith(failingExitScope{e.st}, e.pc, []string{gone.ID}); err != nil || res.Results[gone.ID] != "ErrInternal" {
		t.Errorf("delete with a failing exit-scope read = %+v, %v; want ErrInternal", res.Results, err)
	}
	if got := busyFacts(t, e.dbPath, gone.ID); !reflect.DeepEqual(got, keep[gone.ID].facts) {
		t.Errorf("busy-send facts after the failed deletion = %+v; want unchanged %+v", got, keep[gone.ID].facts)
	}
	e.assertRowUnchanged(t, gone.ID, keep[gone.ID].cols)
	absent := "absent-" + uuid.NewString()[:8]
	calls := len(e.rec.SocketCalls()) + len(e.rec.Calls())
	res, err := adminapi.Delete(c, []string{uncovered.ID, running.ID, uncheckable.ID, gone.ID, neverIssued.ID, intentOnly.ID, helperGone.ID, absent})

	want := map[string]string{uncovered.ID: "ok", running.ID: "ErrSpawnNotDeletable", uncheckable.ID: "ErrSpawnNotDeletable",
		gone.ID: "ok", neverIssued.ID: "ok", intentOnly.ID: "ErrSpawnNotDeletable", helperGone.ID: "ErrSpawnNotDeletable",
		absent: "ErrSpawnNotFound"}
	if err != nil || !reflect.DeepEqual(res.Results, want) {
		t.Fatalf("delete = %v, %v; want %v", res.Results, err, want)
	}
	if n := len(e.rec.SocketCalls()) + len(e.rec.Calls()); n != calls {
		t.Errorf("%d tmux calls during delete; want none", n-calls)
	}
	for _, r := range []killRow{running, uncheckable, intentOnly, helperGone} {
		if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, keep[r.ID].facts) {
			t.Errorf("%s busy-send facts = %+v; want unchanged %+v", r.ID, got, keep[r.ID].facts)
		}
		e.assertRowUnchanged(t, r.ID, keep[r.ID].cols)
	}
	assertRowGone(t, e, uncovered.ID)
	for _, r := range []killRow{gone, neverIssued} {
		assertRowGone(t, e, r.ID)
		if got, want := lcLookup(t, c, "del-"+r.ID), lcNotStarted(r.ID, "del-"+r.ID, "record_deleted"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s Held Message after deletion = %+v; want %+v", r.ID, got, want)
		}
	}
}
