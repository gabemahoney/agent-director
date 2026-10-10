package apitest

// read_surface_settle.go — the read-surface cases with a settle time (b.3ie):
// over SeedReadSurfaceStore's store, whose rows are scrambled to read as
// ReadSurfaceSettled(state), status, get and list must give each case's
// settle-off golden with only the state changed: no field added or dropped, no
// state outside the seven, every other value kept, and list's state filter
// and limit applied to the settled state.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// SettleOnConfig is the config.toml the settle-on surface tests run the
// read-surface cases under: the largest settle time.
var SettleOnConfig = fmt.Sprintf("[state]\nsettle_ms = %d\n", config.MaxStateSettleMs)

// CheckSettleOn fails t unless got, case c's output (one JSON document) under
// SettleOnConfig over SeedReadSurfaceStore's store, is what the settle time
// makes of the case's golden: status and get give the golden with its state
// read as its settled state; list gives, in seeding order, the rows of the
// unfiltered list golden whose settled state matches c's state filter, at
// most c's limit, each with its state read as settled.
func (cs ReadSurfaceCases) CheckSettleOn(t testing.TB, c ReadSurfaceCase, got string) {
	t.Helper()
	gotDoc := parseJSONDoc(t, c.Name+" output", got)
	var want map[string]any
	switch c.Verb {
	case "status", "get":
		want = settledRow(t, parseJSONDoc(t, c.Name+" golden", cs.Golden(t, c.Name)))
	case "list":
		want = cs.settledList(t, c)
	default:
		t.Fatalf("%s: unknown verb %q", c.Name, c.Verb)
	}
	if !reflect.DeepEqual(gotDoc, want) {
		t.Errorf("%s with a settle time:\n got  %v\n want %v", c.Name, gotDoc, want)
	}
}

// settledList is list case c's expected document with a settle time.
func (cs ReadSurfaceCases) settledList(t testing.TB, c ReadSurfaceCase) map[string]any {
	t.Helper()
	all, _ := parseJSONDoc(t, "list golden", cs.Golden(t, "list"))["spawns"].([]any)
	var filter []string
	if raw, ok := c.Params["state"].([]any); ok {
		for _, s := range raw {
			filter = append(filter, fmt.Sprint(s))
		}
	}
	limit := len(all)
	if l, ok := c.Params["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}
	rows := []any{}
	for _, r := range all {
		row := settledRow(t, r.(map[string]any))
		if len(rows) < limit && (filter == nil || slices.Contains(filter, row["state"].(string))) {
			rows = append(rows, row)
		}
	}
	return map[string]any{"spawns": rows}
}

// settledRow returns a copy of row with its state read as settled
// (ReadSurfaceSettled), failing t when row has no state among the seven.
func settledRow(t testing.TB, row map[string]any) map[string]any {
	t.Helper()
	state, _ := row["state"].(string)
	if !slices.Contains(readSurfaceStates, state) {
		t.Fatalf("golden row %v: state %q is not one of %v", row, state, readSurfaceStates)
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = v
	}
	out["state"] = ReadSurfaceSettled(state)
	return out
}

// SettleFilterRow is one row SeedSettleFilterRows seeds: its id, its raw state
// and the state a read with a settle time reports for it.
type SettleFilterRow struct{ ID, Raw, Reported string }

// SeedSettleFilterRows creates the store at dbPath and seeds, in this order,
// two rows raw waiting whose change from working has not settled (they read
// working), a settled waiting row, and a row raw working whose change from
// waiting has not settled (it reads waiting). The unsettled rows are stamped
// at nowMs: with settle time T they read their settled state until nowMs + T.
// So list --state waiting --limit 1 with a settle time is sf-settled, which a
// filter applied after the limit would miss.
func SeedSettleFilterRows(dbPath string, nowMs int64) ([]SettleFilterRow, error) {
	rows := []SettleFilterRow{
		{"sf-unsettled-1", store.StateWaiting, store.StateWorking},
		{"sf-unsettled-2", store.StateWaiting, store.StateWorking},
		{"sf-settled", store.StateWaiting, store.StateWaiting},
		{"sf-to-working", store.StateWorking, store.StateWaiting},
	}
	for _, r := range rows {
		var opts []SpawnOption
		if r.Reported != r.Raw {
			opts = append(opts, WithSettle(r.Reported, nowMs))
		}
		if _, err := SeedSpawn(dbPath, r.ID, r.Raw, "", "", "", true, opts...); err != nil {
			return nil, fmt.Errorf("SeedSettleFilterRows: %w", err)
		}
	}
	return rows, nil
}

// SettleFilterIDs returns the ids of rows, in seeding order, whose state is one
// of states (Reported when settled, Raw otherwise), at most limit (0: all).
func SettleFilterIDs(rows []SettleFilterRow, settled bool, limit int, states ...string) []string {
	ids := []string{}
	for _, r := range rows {
		state := r.Raw
		if settled {
			state = r.Reported
		}
		if slices.Contains(states, state) && (limit == 0 || len(ids) < limit) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// parseJSONDoc parses doc, one JSON object, failing t otherwise.
func parseJSONDoc(t testing.TB, what, doc string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("%s is not a JSON object: %v: %q", what, err, doc)
	}
	return v
}
