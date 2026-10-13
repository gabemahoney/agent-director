package main_test

// list_subagents_cli_test.go: `list-subagents` prints pkg/api's encoding of the
// row's subagent records on one line, and refuses what `get` refuses (SR-11).
// The old --claude-instance-id refusal and the missing-flag row are
// old_flags_cli_test.go's and advice_follow_cli_test.go's.

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedSubagentRow seeds one waiting row in home's store (tracking subagents unless
// opts say otherwise) and returns its id.
func seedSubagentRow(t *testing.T, home string, opts ...apitest.SpawnOption) string {
	t.Helper()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateWaiting, "", "", uuid.NewString(), true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return id
}

// TestListSubagentsCLIPrintsRecords: a row with a fully known stopped record
// (three time encodings, lost_at null) and a record of nothing but its id
// prints exactly one JSON line, every key present, unknowns null, ordered by
// first_started_at with unknown last.
func TestListSubagentsCLIPrintsRecords(t *testing.T) {
	home := t.TempDir()
	id := seedSubagentRow(t, home)
	for _, sub := range []struct {
		id   string
		opts []apitest.SubagentOption
	}{
		{"sub-bare", nil},
		{"sub-full", []apitest.SubagentOption{
			apitest.WithSubagentState("stopped"), apitest.WithSubagentCloseReason("stop_event"),
			apitest.WithSubagentSessionID("sess-1"), apitest.WithSubagentType("Explore"),
			apitest.WithSubagentDescription("map the repo"), apitest.WithSubagentModel("opus"),
			apitest.WithSubagentParent("agent", id), apitest.WithSubagentToolUseID("toolu_1"),
			apitest.WithSubagentRunMode("foreground"),
			apitest.WithSubagentFirstStartedAt(1790000000000), // whole second: no fraction
			apitest.WithSubagentStartedAt(1790000000120),      // trailing zero dropped
			apitest.WithSubagentStoppedAt(1790000000456),
		}},
	} {
		if err := apitest.SeedSubagent(stateDB(home), id, sub.id, sub.opts...); err != nil {
			t.Fatalf("SeedSubagent %s: %v", sub.id, err)
		}
	}

	stdout, stderr, code := runCLIWithHome(t, home, "list-subagents", "--instance-id", id)

	want := `{"tracked":true,"subagents":[` +
		`{"subagent_id":"sub-full","instance_id":"` + id + `","session_id":"sess-1","subagent_type":"Explore",` +
		`"description":"map the repo","model":"opus","parent_type":"agent","parent_id":"` + id + `",` +
		`"tool_use_id":"toolu_1","run_mode":"foreground","first_started_at":"2026-09-21T14:13:20Z",` +
		`"started_at":"2026-09-21T14:13:20.12Z","stopped_at":"2026-09-21T14:13:20.456Z","lost_at":null,` +
		`"state":"stopped","close_reason":"stop_event"},` +
		`{"subagent_id":"sub-bare","instance_id":"` + id + `","session_id":null,"subagent_type":null,` +
		`"description":null,"model":null,"parent_type":null,"parent_id":null,"tool_use_id":null,"run_mode":null,` +
		`"first_started_at":null,"started_at":null,"stopped_at":null,"lost_at":null,` +
		`"state":"running","close_reason":null}]}` + "\n"
	if code != 0 || stdout != want {
		t.Errorf("exit = %d, stdout = %q; want 0 and %q (stderr=%q)", code, stdout, want, stderr)
	}
}

// TestListSubagentsCLIEmpty: a row with no records prints subagents as [], not
// null, with tracked saying whether its launch registered tracking.
func TestListSubagentsCLIEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []apitest.SpawnOption
		want string
	}{
		{"tracked", nil, `{"tracked":true,"subagents":[]}` + "\n"},
		{"pre-upgrade row", []apitest.SpawnOption{apitest.WithNoSubagentTracking()}, `{"tracked":false,"subagents":[]}` + "\n"},
		{"codex", []apitest.SpawnOption{apitest.WithAgent("codex"), apitest.WithNoSubagentTracking()}, `{"tracked":false,"subagents":[]}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			id := seedSubagentRow(t, home, tc.opts...)

			stdout, stderr, code := runCLIWithHome(t, home, "list-subagents", "--instance-id", id)

			if code != 0 || stdout != tc.want {
				t.Errorf("exit = %d, stdout = %q; want 0 and %q (stderr=%q)", code, stdout, tc.want, stderr)
			}
		})
	}
}

// TestListSubagentsCLIUnknownID: an id with no row is ErrSpawnNotFound on
// stderr alone, naming the id.
func TestListSubagentsCLIUnknownID(t *testing.T) {
	const id = "sa-cli-no-such-row"

	stdout, stderr, code := runCLI(t, "list-subagents", "--instance-id", id)

	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotFound")
	if !strings.Contains(env.ErrDescription, id) {
		t.Errorf("err_description = %q; want it to name %q", env.ErrDescription, id)
	}
}

// TestListSubagentsCLIFlagRefusals: an empty, valueless or unknown flag gets
// the ErrInvalidFlags envelope `get` gives.
func TestListSubagentsCLIFlagRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"empty value", []string{"--instance-id="}, "--instance-id is required"},
		{"no value", []string{"--instance-id"}, "flag needs an argument: -instance-id"},
		{"unknown flag", []string{"--bogus", "x"}, "flag provided but not defined: -bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			gStdout, gStderr, gCode := runCLIWithHome(t, home, append([]string{"get"}, tc.args...)...)
			want := assertOnlyEnvelope(t, gStdout, gStderr, gCode, "ErrInvalidFlags")

			stdout, stderr, code := runCLIWithHome(t, home, append([]string{"list-subagents"}, tc.args...)...)

			got := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if got.ErrDescription != want.ErrDescription {
				t.Errorf("err_description = %q; want get's %q", got.ErrDescription, want.ErrDescription)
			}
			if got.ErrDescription != tc.want {
				t.Errorf("err_description = %q; want %q", got.ErrDescription, tc.want)
			}
		})
	}
}
