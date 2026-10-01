package api_test

// lookup_calltable_unusable_test.go is the call-site table's unusable-name
// column family (SR-3.2, SR-6.1, SR-7.2; Epic 19): one column per fixture of
// unusable_name_fixture_test.go, its row (no session) recording that name,
// and one more whose row records no socket while the socket directory is
// unusable, so the guard must come before the socket. Every verb that would
// look the row up refuses with the fixture's ErrInternal, with no tmux call
// and the row unchanged; a state guard that answers first keeps its answer.
// The cells are merged into each verb's row with maps.Copy; -run
// 'CallTable.*/Unusable' selects them. Later verbs merge these cells, never
// their own unusable-name columns.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableUnusable is one column of the family: its outcome, its fixture,
// and noSocket, a row recording no socket in an unusable socket directory.
type callTableUnusable struct {
	outcome  callTableOutcome
	fixture  unusableNameFixture
	noSocket bool
}

// callTableUnusables is the family: each fixture, then the pre-b.gqe default
// name with no recorded socket.
func callTableUnusables() []callTableUnusable {
	var out []callTableUnusable
	var noSocket callTableUnusable
	for _, f := range unusableNameFixtures() {
		out = append(out, callTableUnusable{outcome: callTableOutcome("Unusable name, " + f.label), fixture: f})
		if f.raw == preGqeDefaultName {
			noSocket = callTableUnusable{fixture: f, noSocket: true,
				outcome: callTableOutcome("Unusable name, " + f.label + ", no recorded socket, unusable socket directory")}
		}
	}
	return append(out, noSocket)
}

// callTableUnusableColumns is the family's columns: the row, its agent
// running and no session seeded, records the fixture's raw name.
func callTableUnusableColumns() []callTableColumn {
	var cols []callTableColumn
	for _, u := range callTableUnusables() {
		col := callTableColumn{outcome: u.outcome,
			spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(u.fixture.raw)}}}
		if u.noSocket {
			col.spec.Opts = append(col.spec.Opts, apitest.WithTmuxSocket(""))
			col.world = callTableUnusableSocketDir
		}
		cols = append(cols, col)
	}
	return cols
}

// callTableUnusableSocketDir makes the default socket's directory unusable
// (group- and world-readable), so resolving the socket would refuse.
func callTableUnusableSocketDir(t *testing.T, e *killEnv, _ *killRow) {
	if err := os.Chmod(filepath.Dir(e.defaultSocket), 0o755); err != nil {
		t.Fatalf("chmod socket directory: %v", err)
	}
}

// callTableUnusableRefused is the cells of a verb whose guard refuses an
// unusable recorded name: ErrInternal with the fixture's description and no
// tmux call.
func callTableUnusableRefused() map[callTableOutcome]callTableCell {
	cells := map[callTableOutcome]callTableCell{}
	for _, u := range callTableUnusables() {
		cells[u.outcome] = callTableCell{errName: "ErrInternal",
			desc: func(*killEnv, killRow) apitest.DescCase { return u.fixture.desc }}
	}
	return cells
}

// callTableUnusableNA is the cells of a verb to which no unusable-name column
// applies, with reason.
func callTableUnusableNA(reason string) map[callTableOutcome]callTableCell {
	cells := map[callTableOutcome]callTableCell{}
	for _, u := range callTableUnusables() {
		cells[u.outcome] = callTableCell{na: reason}
	}
	return cells
}
