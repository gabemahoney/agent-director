package installsh_test

// Pre-flight's live-row refusal (SR-2.14 "Pre-flight"): a crossing upgrade
// with no move due refuses while its store holds a row in a live state
// (agents_live), or when those rows cannot be read (agent_rows_unreadable),
// changing nothing. With a move due, the move's own check refuses first with
// ErrAgentsLive, as move_preflight_test.go's live-row rows pin.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// crossingLiveRowSQL (reusable test fixture, with the genuine-release
// fixture) is the SQL that adds the row id in state to a genuine v0.16.0 or
// v0.17.3 store: a Claude agent whose recorded process is gone, as after a
// crash, so that release's find-missing marks it missing. A case that needs
// such a row uses it, never SQL of its own.
func crossingLiveRowSQL(id, state string) string {
	return "INSERT INTO spawns (instance_id, state, cwd, tmux_session_name, relay_mode, agent, pid, proc_starttime) " +
		"VALUES ('" + id + "', '" + state + "', '/tmp', 'ad-" + id + "', 'off', 'claude', 4194000, '1');\n"
}

// liveRow is a row a store holds in a live state.
type liveRow struct{ id, state string }

// assertLiveRefused checks res is exactly pre-flight's agents_live refusal of
// rows (in instance id order) in c's store, with the home as in before. It
// returns the commands the refusal advises: each row's stop command, then
// find-missing.
func (c *crossingCase) assertLiveRefused(t *testing.T, res moveResult, rows []liveRow, before moveTree) (stops []string, findMissing string) {
	t.Helper()
	bin := filepath.Join(c.h.New, "bin", "agent-director")
	if !schedBareWord.MatchString(bin) {
		t.Fatalf("the installed binary's path %q needs shell quoting", bin)
	}
	lines := []string{"install.sh: state.db holds live agents, which the native-input cutover cannot cross; refusing to upgrade.",
		"  state.db: " + c.db,
		"  Rows in a live state (state, instance id), each followed by the command that stops its agent:"}
	for _, r := range rows {
		verb := "kill"
		if r.state == "waiting" {
			verb = "pause"
		}
		stops = append(stops, bin+" "+verb+" --instance-id "+r.id)
		lines = append(lines, "    "+r.state+" "+r.id, "      "+stops[len(stops)-1])
	}
	findMissing = bin + " find-missing"
	lines = append(lines, "  Stop whatever starts agents (a scheduler, a loop, a caller) first, so no new one starts.",
		"  Only for an agent you have established is gone, clear its row with the installed binary's find-missing, run as the agents' user in their tmux environment:",
		"    "+findMissing,
		"  A missing row does not prove the agent stopped: a run as another user or against another tmux server can mark a running agent missing (docs/maintenance.md \"Scheduling the sweeps\").",
		"  Nothing was installed or changed. Then re-run the same install command from a shell AD did not start.")
	action := "stop each live agent with " + bin + " pause --instance-id <id> (a waiting row) or " + bin +
		" kill --instance-id <id> (any other live row); only for an agent you have established is gone, clear its row with " + bin +
		" find-missing, run as the agents' user in their tmux environment (a missing row does not prove the agent stopped: docs/maintenance.md \"Scheduling the sweeps\"); then re-run the same install command from a shell AD did not start"
	envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
	assertOutcomeEnvelope(t, envs[len(envs)-1], "ErrJournalMigrationRefused", "agents_live", action, "not_committed", nil)
	if want := strings.Join(append(lines, envs[len(envs)-1], res.Last()), "\n") + "\n"; res.Stderr != want {
		t.Errorf("stderr:\n%s\nwant:\n%s", res.Stderr, want)
	}
	assertRefusedUnchanged(t, c.h, before)
	return stops, findMissing
}

// TestAdviceFollow_J46_CrossingLiveRows: from a genuine v0.17.3 install (with
// a managed PATH link and a leftover sentinel) or v0.16.0 one, a crossing
// upgrade whose store holds a row in a live state, its agent gone, refuses in
// pre-flight with nothing changed and the release's list still working; an
// ended row is not named. Followed literally in the file and piped forms,
// each stop command leaves a gone agent's row, so the same refusal comes
// back; for those agents, established gone (their recorded process is not
// running), the release's find-missing, run as their user in their
// environment, marks the rows missing, and the same install command then
// crosses. J46: "stop each live agent with <bin> pause --instance-id <id> (a
// waiting row) or <bin> kill --instance-id <id> (any other live row); only
// for an agent you have established is gone, clear its row with <bin>
// find-missing, run as the agents' user in their tmux environment (...);
// then re-run the same install command from a shell AD did not start".
func TestAdviceFollow_J46_CrossingLiveRows(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3 and v0.16.0")
	}
	type liveCase struct {
		name, tag     string
		rows          []liveRow
		piped, bash32 bool
	}
	two := []liveRow{{"a-waiting", "waiting"}, {"b-working", "working"}}
	var cases []liveCase
	for _, s := range []string{"pending", "waiting", "working", "check_permission", "ask_user"} {
		cases = append(cases, liveCase{name: s, tag: "v0.17.3", rows: []liveRow{{"row-" + s, s}}})
	}
	cases = append(cases, liveCase{name: "piped", tag: "v0.17.3", rows: two, piped: true},
		liveCase{name: "bash-3.2", tag: "v0.17.3", rows: two, bash32: true},
		liveCase{name: "v0.16.0", tag: "v0.16.0", rows: two})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c *crossingCase
			if tc.tag == "v0.17.3" {
				c, _ = newCopiesCrossing(t)
			} else {
				c = newCrossingCaseOf(t, tc.tag)
			}
			h := c.h
			if tc.bash32 {
				bash := os.Getenv("AD_TEST_BASH32")
				if bash == "" {
					t.Skip("SKIP: bash 3.2.57 is not available (AD_TEST_BASH32 unset)")
				}
				s := *h.S
				s.Bash = bash
				h.S = &s
			}
			seed := crossingLiveRowSQL("row-ended", "ended")
			var ids []string
			for _, r := range tc.rows {
				seed += crossingLiveRowSQL(r.id, r.state)
				ids = append(ids, r.id)
			}
			h.Sqlite(c.db, seed)
			flags := c.valid()
			install := func() moveResult {
				if tc.piped {
					return schedRunArgv(t, h, h.S.Install, true, schedArgv(h, flags...))
				}
				return h.Install(flags...)
			}
			before := h.PhysicalSnapshot()
			res := install()
			stops, findMissing := c.assertLiveRefused(t, res, tc.rows, before)
			if _, err := os.Lstat(journalBackup(flags)); err == nil {
				t.Errorf("the refusal made the backup %s", journalBackup(flags))
			}
			if got := h.Run(c.prev, "list"); got.Code != 0 {
				t.Errorf("%s's list after the refusal: exit %d: %s", tc.tag, got.Code, got.Stderr)
			}

			sh := newOpsShell(h)
			for i, stop := range stops {
				got := sh.Run(stop)
				ok := got.Code == 0 && strings.TrimSpace(got.Stdout) == `{"kill_sent":false}`
				if tc.rows[i].state == "waiting" {
					ok = got.Code == 1 && strings.Contains(got.Stderr, `"err_name":"ErrTmuxSendKeys"`)
				}
				if !ok {
					t.Errorf("%s: exit %d, stdout %q, stderr %q; want a gone agent's answer", stop, got.Code, got.Stdout, got.Stderr)
				}
			}
			before = h.PhysicalSnapshot()
			if again := install(); again.Code != res.Code || again.Stderr != res.Stderr {
				t.Errorf("re-run with the gone agents' rows still live: exit %d\n%s\nwant the same refusal", again.Code, again.Stderr)
			}
			assertRefusedUnchanged(t, h, before)

			got := sh.Run(findMissing)
			var found struct {
				IDs []string `json:"ids"`
			}
			if err := json.Unmarshal([]byte(got.Stdout), &found); got.Code != 0 || err != nil {
				t.Fatalf("%s: exit %d, stdout %q, stderr %q", findMissing, got.Code, got.Stdout, got.Stderr)
			}
			sort.Strings(found.IDs)
			if !reflect.DeepEqual(found.IDs, ids) {
				t.Errorf("find-missing marked %q; want %q", found.IDs, ids)
			}
			if got := h.Sqlite(c.db, "SELECT count(*) FROM spawns WHERE state NOT IN ('missing', 'ended');", "-readonly"); got != "0\n" {
				t.Errorf("%q rows are neither missing nor ended after find-missing", got)
			}
			c.assertCrossed(t, install(), journalBackup(flags))
		})
	}

	// "re-run the same install command once state.db is no longer busy (a
	// re-run reads its rows again); if it still cannot be read, inspect
	// <store> with the error this run reported": the re-run refuses the same
	// while the rows cannot be read, and crosses once the column the error
	// names is back. A busy store never reaches this check: the version gate
	// refuses it first, with ErrVersionUnreadable.
	t.Run("unreadable-rows", func(t *testing.T) {
		c := newCrossingCase(t)
		h := c.h
		h.Sqlite(c.db, "ALTER TABLE spawns RENAME COLUMN state TO state_gone;")
		flags := c.valid()
		before := h.PhysicalSnapshot()
		res := h.Install(flags...)
		assertOwnRefusal(t, res, "agent_rows_unreadable",
			"re-run the same install command once state.db is no longer busy (a re-run reads its rows again); if it still cannot be read, inspect "+c.db+" with the error this run reported",
			"install.sh: cannot read state.db's agent rows, so cannot tell whether an agent is live before the native-input cutover; refusing to upgrade.",
			"  state.db: "+c.db,
			"  The rows could not be read (the store was busy past its busy timeout of 10000 ms, or is unreadable). sqlite3 said:")
		if !strings.Contains(res.Stderr, "no such column: state\n") {
			t.Errorf("stderr lacks sqlite3's error:\n%s", res.Stderr)
		}
		assertRefusedUnchanged(t, h, before)
		if again := h.Install(flags...); again.Code != res.Code || again.Stderr != res.Stderr {
			t.Errorf("re-run with the rows still unreadable: exit %d\n%s\nwant the same refusal", again.Code, again.Stderr)
		}
		assertRefusedUnchanged(t, h, before)
		h.Sqlite(c.db, "ALTER TABLE spawns RENAME COLUMN state_gone TO state;")
		c.assertCrossed(t, h.Install(flags...), journalBackup(flags))
	})
}
