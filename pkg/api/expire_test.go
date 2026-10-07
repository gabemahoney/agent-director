package api_test

// expire_test.go covers expire's selection by the retention window on the
// fixture clock (SR-12.1), never a live or NULL-ended_at row, its two
// store-error paths (SR-20.6), the result shape (SR-12.4) and Client.Expire.
// Per-row reasons, the conditional delete, the sweep, odd rows, the trail and
// the call table have their own files.

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expireRetention is config's default expire retention window.
var expireRetention = time.Duration(config.Default().Defaults.ExpireRetentionDays) * 24 * time.Hour

// seedUnselectable seeds a live row and a finished row with a NULL ended_at,
// both with dead agents and no session; expire must never select either.
func (e *killEnv) seedUnselectable(t *testing.T) []killRow {
	t.Helper()
	rows := []killRow{
		e.seedRow(t, killRowSpec{ID: "live", Agent: agentGone, NoSession: true}),
		e.seedRow(t, killRowSpec{ID: "null-ended-at", State: store.StateMissing, Agent: agentGone, NoSession: true}),
	}
	if got := e.columns(t, "null-ended-at").EndedAt; got != nil {
		t.Fatalf("null-ended-at row ended_at = %#v; want NULL", got)
	}
	return rows
}

// assertStillStored fails unless every one of rows is still in the store.
func (e *killEnv) assertStillStored(t *testing.T, rows ...killRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := e.st.GetSpawn(r.ID); err != nil {
			t.Errorf("row %s: %v; want it kept in the store", r.ID, err)
		}
	}
}

// TestExpireSelection checks the window (override, else config's retention)
// selects finished rows by ended_at on the fixture clock; an explicit zero
// selects all; a live row and a NULL-ended_at row are never selected. A
// negative override is refused (TestExpireOnlyExplicitZeroSelectsEvery).
func TestExpireSelection(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	ages := map[string]time.Duration{
		"ended-2h":              2 * time.Hour,
		"missing-2h":            2 * time.Hour,
		"ended-10m":             10 * time.Minute,
		"ended-under-retention": expireRetention - time.Hour,
		"ended-past-retention":  expireRetention + time.Hour,
		"ended-after-clock":     -time.Hour, // a store write ahead of the injected clock
	}
	every := []string{"ended-2h", "missing-2h", "ended-10m", "ended-under-retention", "ended-past-retention",
		"ended-after-clock"}
	cases := []struct {
		name string
		over *time.Duration
		want []string // deleted
	}{
		{"older than 1h", olderThan(time.Hour),
			[]string{"ended-2h", "missing-2h", "ended-under-retention", "ended-past-retention"}},
		{"older than 24h", olderThan(24 * time.Hour), []string{"ended-under-retention", "ended-past-retention"}},
		{"default retention from config", nil, []string{"ended-past-retention"}},
		{"zero override", olderThan(0), every},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			for id, age := range ages {
				spec := e.finishedSpec(age, agentGone)
				spec.ID = id
				if strings.HasPrefix(id, "missing") {
					spec.State = store.StateMissing
				}
				e.seedRow(t, spec)
			}
			never := e.seedUnselectable(t)
			mark := trailMark(t)
			res, lg, err := e.expire(c.over)
			if err != nil {
				t.Fatalf("Expire: %v", err)
			}
			want := map[string]string{}
			for _, id := range c.want {
				want[id] = ""
			}
			assertExpired(t, res, mark, want)
			e.assertLookupsOn(t, apitest.TestSocket)
			for id := range ages {
				_, err := e.st.GetSpawn(id)
				if gone := errors.Is(err, store.ErrSpawnNotFound); gone != slices.Contains(c.want, id) {
					t.Errorf("row %s: GetSpawn err = %v; deleted want %v", id, err, slices.Contains(c.want, id))
				}
			}
			e.assertStillStored(t, never...)
			if len(lg.lines) != 0 {
				t.Errorf("log = %q; want none", lg.lines)
			}
		})
	}
}

// TestExpireStoreErrors (SR-20.6): a failed candidate read fails the verb with
// no tmux call and no record; a failed per-row delete keeps that row
// store_error and the run goes on. Either is logged once, naming the error and
// the row, never its session environment; a nil logger changes no outcome.
func TestExpireStoreErrors(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	const secret = "expire-secret-env-value"
	for _, tc := range []struct {
		name               string
		failList, noLogger bool
	}{
		{"candidate read", true, false},
		{"candidate read, nil logger", true, true},
		{"per-row delete", false, false},
		{"per-row delete, nil logger", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			seed := func(id string, opts ...apitest.SpawnOption) killRow {
				spec := e.finishedSpec(2*time.Hour, agentGone, opts...)
				spec.ID = id
				return e.seedRow(t, spec)
			}
			a, b, c := seed("del-a"), seed("del-b", apitest.WithExtraEnv(map[string]string{"EXPIRE_SECRET": secret})), seed("del-c")
			w := e.expireStore()
			if tc.failList {
				w.failList(nil)
			} else {
				w.failDelete(b.ID, nil)
			}
			lg := &recordingLogger{}
			var logger api.ExpireLogger = lg
			if tc.noLogger {
				logger = nil
			}
			mark := trailMark(t)
			res, err := api.Expire(w, e.rec, e.pc, config.Default().Defaults.ExpireRetentionDays, olderThan(0),
				e.cfg.EffectiveSweepBudget(), e.clock.Now, logger)
			if tc.failList {
				if !errors.Is(err, errInjectedStore) {
					t.Fatalf("Expire err = %v; want %v", err, errInjectedStore)
				}
				e.assertLookupsOn(t)
				for _, r := range []killRow{a, b, c} {
					assertNoTrailSince(t, mark, r.ID)
				}
				e.assertStillStored(t, a, b, c)
			} else {
				if err != nil {
					t.Fatalf("Expire: %v; want the run to succeed", err)
				}
				assertExpired(t, res, mark, map[string]string{a.ID: "", b.ID: "store_error", c.ID: ""})
				e.assertLookupsOn(t, apitest.TestSocket)
				e.assertStillStored(t, b)
			}
			if tc.noLogger {
				return
			}
			if len(lg.lines) != 1 || !strings.Contains(lg.lines[0], errInjectedStore.Error()) ||
				(!tc.failList && !strings.Contains(lg.lines[0], b.ID)) {
				t.Errorf("log = %q; want one line naming %q (and the failed row %s)", lg.lines, errInjectedStore, b.ID)
			}
			if all := strings.Join(lg.lines, "\n"); strings.Contains(all, secret) || strings.Contains(all, "EXPIRE_SECRET") {
				t.Errorf("log %q carries the row's session environment", all)
			}
		})
	}
}

// TestExpireResultShape checks count/ids and kept/kept_ids agree, both lists
// are sorted, and JSON encodes them as [] when empty (SR-12.4).
func TestExpireResultShape(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	t.Run("mixed", func(t *testing.T) {
		e := newKillEnv(t)
		seed := func(id string, a agentState, session bool) {
			spec := e.finishedSpec(2*time.Hour, a)
			spec.ID, spec.NoSession = id, !session
			e.seedRow(t, spec)
		}
		seed("shape-d", agentGone, false)
		seed("shape-b", agentAlive, false)
		seed("shape-c", agentGone, true)
		seed("shape-a", agentGone, false)
		mark := trailMark(t)
		res, _, err := e.expire(olderThan(time.Hour))
		if err != nil {
			t.Fatalf("Expire: %v", err)
		}
		assertExpired(t, res, mark, map[string]string{"shape-a": "", "shape-d": "",
			"shape-b": "process_alive", "shape-c": "ours"})
		assertExpireJSON(t, res, `{"count":2,"ids":["shape-a","shape-d"],"kept":2,"kept_ids":["shape-b","shape-c"]}`)
	})
	t.Run("empty", func(t *testing.T) {
		e := newKillEnv(t)
		res, _, err := e.expire(olderThan(0))
		if err != nil {
			t.Fatalf("Expire: %v", err)
		}
		assertExpireJSON(t, res, `{"count":0,"ids":[],"kept":0,"kept_ids":[]}`)
	})
}

// assertExpireJSON fails unless res encodes as want.
func assertExpireJSON(t *testing.T, res api.ExpireResult, want string) {
	t.Helper()
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(got) != want {
		t.Errorf("JSON = %s; want %s", got, want)
	}
}

// TestClientExpireDefaultRetention checks Client.Expire with no override
// applies the configured retention and returns kept and kept_ids; a deleted
// row's transcript file stays on disk.
func TestClientExpireDefaultRetention(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	const sid = "expire-transcript-session"
	transcript := apitest.SeedJsonl(t, "/tmp", sid)
	goneSpec := e.finishedSpec(expireRetention+time.Hour, agentGone, apitest.WithJsonlPath(transcript))
	goneSpec.SessionID = sid
	gone := e.seedRow(t, goneSpec)
	ours := e.finishedSpec(expireRetention+time.Hour, agentGone)
	ours.NoSession = false
	kept := e.seedRow(t, ours)
	young := e.seedFinished(t, expireRetention-time.Hour, agentGone)
	mark := trailMark(t)
	res, logs, err := e.expireClient(t, nil)
	if err != nil {
		t.Fatalf("Client.Expire: %v", err)
	}
	assertExpired(t, res, mark, map[string]string{gone.ID: "", kept.ID: "ours"})
	e.assertStillStored(t, kept, young)
	if logs != "" {
		t.Errorf("Client log = %q; want none", logs)
	}
	if _, err := os.Stat(transcript); err != nil {
		t.Errorf("transcript %s: %v; want it left in place", transcript, err)
	}
}
