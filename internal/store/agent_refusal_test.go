package store_test

// The row's agent write rule (SR-3.1, SR-3.6): an insert or a reuse reset names
// exactly claude or codex, else it is refused with ErrRowAgentInvalid and
// writes nothing. Over reuse_test.go's seedReuseRow and reuseFresh and
// reuse_refused_test.go's reuseStateOf.

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// agentInsert is the row the insert cases write, named agent.
func agentInsert(agent string) store.Spawn {
	return store.Spawn{ClaudeInstanceID: "ag-new", Agent: agent, CWD: "/tmp", TmuxSessionName: "ts-ag-new", RelayMode: "off"}
}

// TestAgentWriteRefused checks an empty, unknown, wrongly cased or padded
// agent is refused by an insert, which leaves no row, and by a reuse reset,
// which leaves the row, its history, its requests and its child as they were.
func TestAgentWriteRefused(t *testing.T) {
	for _, agent := range []string{"", "gemini", "Claude", "CODEX", " claude", "codex ", "claude\n"} {
		t.Run(strconv.Quote(agent), func(t *testing.T) {
			f := newV5Store(t)
			err := f.s.InsertPending(agentInsert(agent))
			if !errors.Is(err, store.ErrRowAgentInvalid) || !strings.Contains(err.Error(), strconv.Quote(agent)) {
				t.Errorf("InsertPending(agent %q) = %v; want ErrRowAgentInvalid naming the agent", agent, err)
			}
			if _, err := f.s.GetSpawn("ag-new"); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("GetSpawn after the refused insert = %v; want ErrSpawnNotFound", err)
			}
			if _, err := apitest.ReadSpawnColumns(f.path, "ag-new"); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("raw read after the refused insert = %v; want no row", err)
			}
			if rows, err := f.s.ListSpawns(store.ListFilters{}); err != nil || len(rows) != 0 {
				t.Errorf("ListSpawns after the refused insert = %d rows, %v; want none", len(rows), err)
			}

			r := seedReuseRow(t, f, reuseSpec{})
			before := f.reuseStateOf(t, r)
			fresh := reuseFresh(r.parent, false)
			fresh.Agent = agent
			res, archived, v, err := f.reuseReset(r, fresh)
			if !errors.Is(err, store.ErrRowAgentInvalid) || res != 0 || archived != "" || v != 0 {
				t.Errorf("ResetForReuse(agent %q) = %v, %q, %d, %v; want ErrRowAgentInvalid and no outcome", agent, res, archived, v, err)
			}
			if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
				t.Errorf("a refused reset wrote:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}

// TestAgentAcceptedAndRead checks claude and codex are accepted by an insert
// and a reset and read back as the row's agent raw, through GetSpawn and
// ListSpawns; a new row has no end report and a seeded one reads back through
// the row's end-report field.
func TestAgentAcceptedAndRead(t *testing.T) {
	const endReported int64 = 1790000123456
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		t.Run(agent, func(t *testing.T) {
			f := newV5Store(t)
			if err := f.s.InsertPending(agentInsert(agent)); err != nil {
				t.Fatalf("InsertPending(agent %q): %v", agent, err)
			}
			seeded := f.seed(store.StateEnded, "", apitest.WithAgent(agent), apitest.WithEndReportedAt(endReported))
			r := seedReuseRow(t, f, reuseSpec{})
			fresh := reuseFresh("", false)
			fresh.Agent = agent
			if res, _, _, err := f.reuseReset(r, fresh); err != nil || res != store.CondApplied {
				t.Fatalf("ResetForReuse(agent %q) = %v, %v; want CondApplied", agent, res, err)
			}
			for _, tc := range []struct {
				name, id string
				endMs    int64
				raw      any
			}{
				{"inserted row, no end report", "ag-new", 0, nil},
				{"seeded end report", seeded, endReported, endReported},
				{"reset row, end report cleared", r.id, 0, nil},
			} {
				if c := f.rawColumns(tc.id); c.Agent != agent || c.EndReportedAt != tc.raw {
					t.Errorf("%s: raw agent, end_reported_at = %v, %v; want %q, %v", tc.name, c.Agent, c.EndReportedAt, agent, tc.raw)
				}
				for _, read := range spawnReads {
					if sp, err := read.read(f.s, tc.id); err != nil || sp.Agent != agent || sp.EndReportedAtMillis != tc.endMs {
						t.Errorf("%s: %s agent, EndReportedAtMillis = %q, %d, %v; want %q, %d", tc.name, read.name, sp.Agent, sp.EndReportedAtMillis, err, agent, tc.endMs)
					}
				}
			}
		})
	}
}
