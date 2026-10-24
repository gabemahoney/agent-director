package apitest

// b.zpv: the read contract every surface (Go, CLI, MCP) keeps for a row that
// shows a Displayed State. state is the row's real lifecycle state, as in
// 0.14-0.15; displayed_state carries the Displayed State while one shows;
// list --state matches the real or the displayed state, before --limit.
// Failures are prefixed "ZPV-FAIL".

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// LifecycleStates are the real states state may carry on any read.
var LifecycleStates = []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser,
	store.StateCheckPermission, store.StateEnded, store.StateMissing}

// DisplayedStateContractRows maps SeedDisplayedStateContract's rows (the
// shared surfaces plus "plain", as pkg/api's displaySurfaceClient seeds) to
// their real states. The needs-intelligence-* rows hold a needs_intelligence
// display committed by 0.16/0.17, which is never reported.
var DisplayedStateContractRows = map[string]string{
	"plain":                     store.StateCheckPermission,
	"needs-intelligence-claude": store.StateCheckPermission,
	"needs-intelligence-codex":  store.StateCheckPermission,
	"needs-intelligence-cursor": store.StateCheckPermission,
	"ordinary":                  store.StateWorking,
	"idle-screen-claude":        store.StateWorking,
	"idle-screen-codex":         store.StateWorking,
	"ordinary-cursor":           store.StateWorking,
}

// displayedStateContractShown maps the rows of DisplayedStateContractRows
// that show a Displayed State to it.
var displayedStateContractShown = map[string]string{
	"idle-screen-claude": store.StateIdleScreen,
	"idle-screen-codex":  store.StateIdleScreen,
}

// SeedDisplayedStateContract seeds DisplayedStateContractRows' rows into a
// new store at dbPath; read them under SettleOnConfig.
func SeedDisplayedStateContract(dbPath string) error {
	if err := SeedNeedsIntelligenceSurface(dbPath); err != nil {
		return err
	}
	if err := SeedIdleScreenSurface(dbPath); err != nil {
		return err
	}
	_, err := SeedSpawn(dbPath, "plain", store.StateCheckPermission, "", "on", "", false)
	return err
}

// DisplayedStateSurface reads one surface's JSON for CheckDisplayedStateContract.
type DisplayedStateSurface struct {
	// Name labels the surface in failures.
	Name string
	// Read returns verb's (status or get) result for id.
	Read func(verb, id string) []byte
	// List returns the list result ({"spawns": [...]}) for states (nil: no
	// filter) and limit (0: no cap).
	List func(states []string, limit int) []byte
}

// displayedStateContractRequests maps the rows of DisplayedStateContractRows
// with an open permission request to its token; get and list rows carry
// exactly it in permission_requests, and every other row none.
var displayedStateContractRequests = map[string]string{
	"needs-intelligence-claude": storefix.TestRequestTokenA,
	"needs-intelligence-codex":  storefix.TestRequestTokenA,
	"needs-intelligence-cursor": storefix.TestRequestTokenA,
}

// CheckDisplayedStateContract checks s over SeedDisplayedStateContract's rows:
// every status, get and list row reads its real state and its Displayed State
// only in displayed_state, and every get and list row its open requests; each
// lifecycle --state, idle_screen, needs_intelligence, working+idle_screen and
// check_permission+idle_screen lists exactly its rows, before --limit.
//
// A row showing a Displayed State keeps it only for settle_ms of real time
// from the read that first observes it, so every read that may observe one
// (its status or get, a list it may be in, and a list naming a Displayed
// State, which fetches every display candidate) runs last.
func CheckDisplayedStateContract(t testing.TB, s DisplayedStateSurface) {
	t.Helper()
	ids := contractRowsWhere(func(string) bool { return true })
	shown := func(id string) bool { return displayedStateContractShown[id] != "" }
	type listCase struct{ states, want []string }
	lists := []listCase{{nil, ids}}
	for _, state := range LifecycleStates {
		lists = append(lists, listCase{[]string{state}, contractRowsWhere(func(id string) bool { return DisplayedStateContractRows[id] == state })})
	}
	lists = append(lists,
		listCase{[]string{store.StateIdleScreen}, contractRowsWhere(func(id string) bool { return displayedStateContractShown[id] == store.StateIdleScreen })},
		listCase{[]string{store.StateNeedsIntelligence}, nil},
		listCase{[]string{store.StateWorking, store.StateIdleScreen}, contractRowsWhere(func(id string) bool { return DisplayedStateContractRows[id] == store.StateWorking })},
		listCase{[]string{store.StateCheckPermission, store.StateIdleScreen}, contractRowsWhere(func(id string) bool {
			return DisplayedStateContractRows[id] == store.StateCheckPermission || displayedStateContractShown[id] == store.StateIdleScreen
		})},
	)
	observesShown := func(c listCase) bool {
		if c.states == nil || containsString(c.states, store.StateIdleScreen) || containsString(c.states, store.StateNeedsIntelligence) {
			return true
		}
		for _, id := range c.want {
			if shown(id) {
				return true
			}
		}
		return false
	}
	for _, last := range []bool{false, true} {
		for _, id := range ids {
			if shown(id) != last {
				continue
			}
			for _, verb := range []string{"status", "get"} {
				label := fmt.Sprintf("%s %s %s", s.Name, verb, id)
				raw := s.Read(verb, id)
				CheckRealStateWire(t, label, raw, verb, DisplayedStateContractRows[id], displayedStateContractShown[id])
				if verb == "get" {
					checkContractRequests(t, label, raw, id)
				}
			}
		}
		for _, c := range lists {
			if observesShown(c) == last {
				checkContractList(t, s, c.states, c.want)
			}
		}
	}
}

// contractRowsWhere returns DisplayedStateContractRows' ids that keep, sorted.
func contractRowsWhere(keep func(id string) bool) []string {
	var out []string
	for id := range DisplayedStateContractRows {
		if keep(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// checkContractList lists states with no cap, then, when want has more than
// one row, capped at 1, requiring exactly want (one of want under the cap),
// each row read as CheckRealStateWire requires and carrying its open
// requests.
func checkContractList(t testing.TB, s DisplayedStateSurface, states []string, want []string) {
	t.Helper()
	limits := []int{0}
	if len(want) > 1 {
		limits = append(limits, 1)
	}
	for _, limit := range limits {
		label := fmt.Sprintf("%s list --state %v --limit %d", s.Name, states, limit)
		raw := s.List(states, limit)
		var listed struct {
			Spawns []json.RawMessage `json:"spawns"`
		}
		if err := json.Unmarshal(raw, &listed); err != nil {
			t.Fatalf("ZPV-FAIL %s: %v: %s", label, err, raw)
		}
		got := []string{}
		for _, row := range listed.Spawns {
			var r struct {
				ID string `json:"instance_id"`
			}
			if err := json.Unmarshal(row, &r); err != nil {
				t.Fatalf("ZPV-FAIL %s: %v: %s", label, err, row)
			}
			got = append(got, r.ID)
			state, ok := DisplayedStateContractRows[r.ID]
			if !ok {
				t.Errorf("ZPV-FAIL %s listed unknown row %s", label, row)
				continue
			}
			CheckRealStateWire(t, label+" row "+r.ID, row, "list", state, displayedStateContractShown[r.ID])
			checkContractRequests(t, label+" row "+r.ID, row, r.ID)
		}
		sort.Strings(got)
		if limit == 1 {
			if len(got) != 1 || !containsString(want, got[0]) {
				t.Errorf("ZPV-FAIL %s matched %v; want one of %v", label, got, want)
			}
		} else if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("ZPV-FAIL %s matched %v; want %v", label, got, want)
		}
	}
}

// checkContractRequests fails unless raw, one get or list row of id, carries
// in permission_requests exactly id's open request of
// displayedStateContractRequests, undecided, or none.
func checkContractRequests(t testing.TB, label string, raw []byte, id string) {
	t.Helper()
	var row struct {
		Requests []struct {
			Token    string  `json:"request_token"`
			Decision *string `json:"decision"`
		} `json:"permission_requests"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("ZPV-FAIL %s: %v: %s", label, err, raw)
	}
	token := displayedStateContractRequests[id]
	if token == "" {
		if len(row.Requests) != 0 {
			t.Errorf("ZPV-FAIL %s: permission_requests=%+v; want none: %s", label, row.Requests, raw)
		}
		return
	}
	if len(row.Requests) != 1 || row.Requests[0].Token != token || row.Requests[0].Decision != nil {
		t.Errorf("ZPV-FAIL %s: permission_requests=%+v; want the one open request %s: %s", label, row.Requests, token, raw)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CheckRealStateWire fails unless raw, one status, get or list ("list") row,
// reads its real state and shows displayed: with displayed "" no displayed_state
// key and no detail of either Displayed State (nor status's
// tmux_session_name); with idle_screen, displayed_state idle_screen and, on
// status and get, the shared idle_screen since and unchanged_minutes.
func CheckRealStateWire(t testing.TB, label string, raw []byte, verb, state, displayed string) {
	t.Helper()
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("ZPV-FAIL %s: %v: %s", label, err, raw)
	}
	if row["state"] != state {
		t.Errorf("ZPV-FAIL %s: state=%v; want the real state %s: %s", label, row["state"], state, raw)
	}
	absent := []string{"request_token", "tool_name"}
	switch displayed {
	case "":
		absent = append(absent, "displayed_state", "since", "unchanged_minutes")
		if verb == "status" {
			absent = append(absent, "tmux_session_name")
		}
	case store.StateIdleScreen:
		if row["displayed_state"] != displayed {
			t.Errorf("ZPV-FAIL %s: displayed_state=%v; want %s: %s", label, row["displayed_state"], displayed, raw)
		}
		if verb != "list" {
			wire := LoadIdleScreenWire(t)
			for _, k := range []string{"since", "unchanged_minutes"} {
				if row[k] != wire[k] {
					t.Errorf("ZPV-FAIL %s: %s=%v; want %v: %s", label, k, row[k], wire[k], raw)
				}
			}
		}
	default:
		t.Fatalf("ZPV-FAIL %s: no contract for displayed state %q", label, displayed)
	}
	for _, k := range absent {
		if _, ok := row[k]; ok {
			t.Errorf("ZPV-FAIL %s: carries %s: %s", label, k, raw)
		}
	}
}
