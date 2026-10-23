package api_test

// display_test.go — what the two Displayed States (b.ob8), needs_intelligence
// and idle_screen, share: the clients' observation helpers, the
// ad.spawn.displayed_state_transition record check, and the Go wire and list
// filter over the shared surfaces. needs_intelligence_*_test.go and
// idle_screen_*_test.go hold each state's own tests.

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

var displayAt = time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)

const displayTS = "2006-01-02T15:04:05.000Z"

// displayProbes maps a client to the store its committed rows are read from,
// so the observation helpers can tell whether a read committed.
var displayProbes sync.Map

func watchDisplayClient(c *api.Client, probe apitest.DisplayProbe) { displayProbes.Store(c, probe) }

// isDisplayedState reports whether state is one of the two Displayed States.
func isDisplayedState(state string) bool {
	return state == api.StateNeedsIntelligence || state == api.StateIdleScreen
}

// observeDisplay runs one reader through apitest.ObserveDisplayRead. commit is
// nil for a read that is not expected to commit.
func observeDisplay(t *testing.T, probe apitest.DisplayProbe, id, want string, commit apitest.CommitCheck, read func() (string, api.StateDetail, error)) {
	t.Helper()
	apitest.ObserveDisplayRead(t, probe, id, want, commit, func() (apitest.DisplayReading, error) {
		state, detail, err := read()
		reading := apitest.DisplayReading{State: state, Since: detail.Since, RequestToken: detail.RequestToken, ToolName: detail.ToolName}
		if detail.UnchangedMinutes != nil {
			reading.UnchangedMinutes = *detail.UnchangedMinutes
		}
		return reading, err
	})
}

// coherentDisplay fails unless detail carries exactly state's detail and the
// attach target, as a status showing a Displayed State does.
func coherentDisplay(t *testing.T, state string, detail api.StateDetail, session string) {
	t.Helper()
	ok := detail.Since != "" && session != ""
	switch state {
	case api.StateNeedsIntelligence:
		ok = ok && detail.RequestToken != "" && detail.UnchangedMinutes == nil
	case api.StateIdleScreen:
		ok = ok && detail.RequestToken == "" && detail.ToolName == "" && detail.UnchangedMinutes != nil && *detail.UnchangedMinutes >= 0
	default:
		ok = false
	}
	if !ok {
		t.Fatalf("incoherent %s: %+v session=%q", state, detail, session)
	}
}

// observeStatus is c.Status judged against the committed row: see
// apitest.ObserveDisplayRead. want is the displayed state, ordinary or
// Displayed. commit names the commit the read must make (nil when it makes
// none).
func observeStatus(t *testing.T, c *api.Client, id, want string, commit apitest.CommitCheck) api.StatusResult {
	t.Helper()
	probe, ok := displayProbes.Load(c)
	if !ok {
		t.Fatal("client is not watched: build it with needsIntelligenceClient or watchDisplayClient")
	}
	var got api.StatusResult
	observeDisplay(t, probe.(apitest.DisplayProbe), id, want, commit, func() (string, api.StateDetail, error) {
		var err error
		got, err = c.Status(id)
		return got.State, got.StateDetail, err
	})
	if isDisplayedState(want) {
		coherentDisplay(t, want, got.StateDetail, got.TmuxSessionName)
	} else if got.StateDetail != (api.StateDetail{}) || got.TmuxSessionName != "" {
		t.Fatalf("ordinary row leaked details: %+v", got)
	}
	return got
}

// assertDisplayState reads Status and requires want, as observeStatus. Showing
// a Displayed State not committed before must commit (observation_version
// advances); one already committed must be read back unchanged without any
// commit.
func assertDisplayState(t *testing.T, c *api.Client, id, want string) api.StatusResult {
	t.Helper()
	var commit apitest.CommitCheck
	if isDisplayedState(want) {
		probe, ok := displayProbes.Load(c)
		if !ok {
			t.Fatal("client is not watched: build it with needsIntelligenceClient or watchDisplayClient")
		}
		before, err := probe.(apitest.DisplayProbe).GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		if before.CommittedDisplay().State != want {
			commit = apitest.VersionAdvanced
		}
	}
	return observeStatus(t, c, id, want, commit)
}

// displayEvents returns id's ad.spawn.displayed_state_transition records after mark, failing any
// not of exactly the record's fields (SR-5.1) or of the replaced event; callers on shared ids are serial.
func displayEvents(t *testing.T, mark int, id string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, ev := range readAPITrailLines(t)[mark:] {
		if ev["instance_id"] != id {
			continue
		}
		switch ev["event"] {
		case "ad.spawn.stuck_transition":
			t.Errorf("wrote the removed record: %v", ev)
		case "ad.spawn.displayed_state_transition":
			keys := make([]string, 0, len(ev))
			for k := range ev {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !equalStrings(keys, []string{"event", "instance_id", "new_state", "prior_state", "ts"}) {
				t.Errorf("record fields %v; want exactly event, instance_id, new_state, prior_state and ts: %v", keys, ev)
			}
			events = append(events, ev)
		}
	}
	return events
}

// assertDisplayEvent fails unless ev, one of displayEvents', moved from prior
// to next at at.
func assertDisplayEvent(t *testing.T, ev map[string]any, prior, next string, at time.Time) {
	t.Helper()
	if ev["prior_state"] != prior || ev["new_state"] != next || ev["ts"] != at.UTC().Format(displayTS) {
		t.Errorf("transition=%v; want %s -> %s at %s", ev, prior, next, at.UTC().Format(displayTS))
	}
}

// displaySurfaceClient is a Client under apitest.SettleOnConfig over a store
// holding plain, an ordinary check_permission row, then the shared
// needs_intelligence and idle_screen surfaces, in that order.
func displaySurfaceClient(t *testing.T) *api.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(path, "plain", store.StateCheckPermission, "", "on", "", true); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedNeedsIntelligenceSurface(path); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedIdleScreenSurface(path); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, cfg)
	c, err := api.New(api.Options{StorePath: path, ConfigPath: cfg, TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestDisplayGoWire: status, get and list of the shared needs_intelligence and
// idle_screen surfaces give each state's exact detail (list only the state)
// and every row its agent's idle_screen_availability.
func TestDisplayGoWire(t *testing.T) {
	t.Parallel()
	c := displaySurfaceClient(t)
	marshal := func(v any, err error) []byte {
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, surface := range apitest.DisplayWireSurfaces {
		for _, agent := range surface.Agents {
			surface.Check(t, marshal(c.Status(surface.Prefix+agent)), agent, "status")
			surface.Check(t, marshal(c.Get(surface.Prefix+agent)), agent, "get")
		}
	}
	rows, err := c.List(api.ListParams{State: []string{api.StateNeedsIntelligence, api.StateIdleScreen}, Limit: 5})
	if err != nil || len(rows.Spawns) != 5 {
		t.Fatalf("list=%+v %v; want the 5 displayed rows", rows, err)
	}
	for _, row := range rows.Spawns {
		apitest.CheckListedDisplayWire(t, marshal(row, nil))
	}
	for id, agent := range apitest.DisplayWireOrdinary {
		apitest.CheckOrdinaryWire(t, marshal(c.Status(id)), agent)
	}
}

// TestDisplayListFilter: a list state filter matches the displayed state before
// the limit, so each Displayed State selects its own rows, at most the limit,
// whatever rows the store holds besides them; a row showing either does not
// match its raw state, and the removed state value, which no row displays,
// matches nothing (docs/caller-contract.md "Filtering").
func TestDisplayListFilter(t *testing.T) {
	t.Parallel()
	c := displaySurfaceClient(t)
	for _, tc := range []struct {
		states []string
		limit  int
		want   []string // sorted
	}{
		{[]string{api.StateNeedsIntelligence}, 3, []string{"needs-intelligence-claude", "needs-intelligence-codex", "needs-intelligence-cursor"}},
		{[]string{api.StateIdleScreen}, 2, []string{"idle-screen-claude", "idle-screen-codex"}},
		{[]string{store.StateCheckPermission}, 0, []string{"plain"}},
		{[]string{store.StateWorking}, 0, []string{"ordinary", "ordinary-cursor"}},
		{[]string{api.StateNeedsIntelligence, store.StateCheckPermission}, 0, []string{"needs-intelligence-claude", "needs-intelligence-codex", "needs-intelligence-cursor", "plain"}},
		{[]string{"stuck"}, 0, []string{}},
	} {
		listed, err := c.List(api.ListParams{State: tc.states, Limit: tc.limit})
		if err != nil {
			t.Fatalf("%v: %v", tc.states, err)
		}
		got := []string{}
		for _, row := range listed.Spawns {
			got = append(got, row.InstanceID)
		}
		sort.Strings(got)
		if !equalStrings(got, tc.want) {
			t.Errorf("filter %v limit %d matched %v; want %v", tc.states, tc.limit, got, tc.want)
		}
	}
}
