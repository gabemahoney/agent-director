package realtmux_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expire on real tmux (SRD SR-12.1, SR-12.2, SR-12.5, SR-2.2, SR-20.7): finished
// rows on Epic 10's kill fixture. Each run is the built CLI, so its trail lands
// under the test's HOME: the in-process trail writer keeps the first HOME it saw.

// expireRun is one expire run: its result, its trail records by event and
// instance id, and, when recorded, every tmux call it made.
type expireRun struct {
	Res   api.ExpireResult
	Trail map[string]map[string][]map[string]any
	Calls []recordedCall
}

// expire runs `bin expire --older-than 0d` (every finished row selected) on the
// store; recorded runs tmux through the recording wrapper (/bin/sh: no locale cases).
func (f *killFix) expire(t testing.TB, bin string, recorded bool) expireRun {
	t.Helper()
	args := []string{"--store-path", f.DBPath}
	var log *callLog
	if recorded {
		_, log = newRecordingClient(t)
		args = append(args, "--tmux-command", filepath.Join(log.dir, "tmux-recorder"))
	}
	cmd := exec.Command(bin, append(args, "expire", "--older-than", "0d")...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("expire: %v; stderr %s", err, redact(stderr.String()))
	}
	run := expireRun{Trail: trailRecords(t, filepath.Dir(f.DBPath))}
	if err := json.Unmarshal(stdout.Bytes(), &run.Res); err != nil {
		t.Fatalf("expire stdout %q is not one result: %v", stdout.String(), err)
	}
	if log != nil {
		run.Calls = log.calls(t)
	}
	return run
}

// trailRecords reads the trail in dir by event, then claude_instance_id.
func trailRecords(t testing.TB, dir string) map[string]map[string][]map[string]any {
	t.Helper()
	out := map[string]map[string][]map[string]any{}
	data, err := os.ReadFile(filepath.Join(dir, "ad-trail.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return out
	}
	if err != nil {
		t.Fatalf("read trail: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("trail line is not JSON: %v", err)
		}
		ev, id := fmt.Sprint(rec["event"]), fmt.Sprint(rec["claude_instance_id"])
		if out[ev] == nil {
			out[ev] = map[string][]map[string]any{}
		}
		out[ev][id] = append(out[ev][id], rec)
	}
	return out
}

// assertLists checks the run deleted exactly deleted and kept exactly kept,
// each sorted, with matching counts.
func (r expireRun) assertLists(t testing.TB, deleted, kept []string) {
	t.Helper()
	deleted, kept = slices.Clone(deleted), slices.Clone(kept)
	slices.Sort(deleted)
	slices.Sort(kept)
	if r.Res.IDs == nil || r.Res.KeptIDs == nil {
		t.Errorf("ids %#v, kept_ids %#v; want both lists, [] when empty, never null", r.Res.IDs, r.Res.KeptIDs)
	}
	if r.Res.Count != len(deleted) || !slices.Equal(r.Res.IDs, deleted) {
		t.Errorf("count %d, ids %v; want %d, %v", r.Res.Count, r.Res.IDs, len(deleted), deleted)
	}
	if r.Res.Kept != len(kept) || !slices.Equal(r.Res.KeptIDs, kept) {
		t.Errorf("kept %d, kept_ids %v; want %d, %v", r.Res.Kept, r.Res.KeptIDs, len(kept), kept)
	}
}

// assertKept checks the run wrote exactly one ad.expire.kept for the row,
// with reason, its recorded name and source ad_expire, and no other field.
func (r expireRun) assertKept(t testing.TB, row apitest.SpawnColumns, id, reason string) {
	t.Helper()
	recs := r.Trail["ad.expire.kept"][id]
	if len(recs) != 1 {
		t.Fatalf("row %s has %d ad.expire.kept records, want 1", id, len(recs))
	}
	got := recs[0]
	delete(got, "ts")
	want := map[string]any{"event": "ad.expire.kept", "claude_instance_id": id, "reason": reason,
		"tmux_session_name": fmt.Sprint(row.TmuxSessionName), "source": "ad_expire"}
	if !maps.Equal(got, want) {
		t.Errorf("row %s ad.expire.kept = %v, want %v", id, got, want)
	}
}

// TestExpireNonASCIIAndDollarRowsUnderHostileLocale: under LC_ALL=C and with no locale variables,
// finished rows whose labelled sessions run are kept ours; a sessionless one is deleted.
func TestExpireNonASCIIAndDollarRowsUnderHostileLocale(t *testing.T) {
	bin := buildHookCLI(t, t.TempDir())
	forms := tmuxfix.LocaleForms() // [0] U2/U3's name ü-x, [1] its instance id agent-ü1
	name, idForm := forms[0], forms[1]
	cases := []struct {
		desc string
		set  []string // KEY=VALUE set after every locale variable is removed
	}{
		{desc: "LC_ALL=C", set: []string{"LC_ALL=C"}},
		{desc: "no locale variables"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			clearLocale(t)
			for _, kv := range tc.set {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			f := newKillFix(t) // a fresh private socket: the create starts its server under this locale
			// No pane or process recorded, so each row reaches the lookup.
			rows := []killRow{f.liveRow(t, killRowSpec{Name: name.Exact, InstanceID: newInstanceID(idForm.Exact),
				State: "ended", NoPane: true})}
			for _, n := range dollarBackslashNames(t) {
				rows = append(rows, f.liveRow(t, killRowSpec{Name: n.Raw, State: "ended", NoPane: true}))
			}
			if out := f.raw().withoutU().must(t, "list-sessions", "-F", "#{session_name}"); strings.Contains(out, name.Exact) {
				t.Fatalf("list-sessions without -u shows %q exactly: the case's locale is not in effect", name.Exact)
			}
			srv := rows[0].Server
			gone := newInstanceID("agent")
			f.seedRow(t, gone, uniqueName(), "ended", store.LaunchIdentity{Token: newToken(t), Socket: f.Socket,
				ServerPID: srv.PID, ServerStart: srv.Start, ServerStarttime: srv.Starttime})
			world := idtWorld(t, f.realTmux)

			run := f.expire(t, bin, false)
			var kept []string
			for _, r := range rows {
				kept = append(kept, r.InstanceID)
				run.assertKept(t, r.Before, r.InstanceID, "ours")
				f.assertRowUnchanged(t, r.InstanceID, r.Before)
			}
			run.assertLists(t, []string{gone}, kept)
			if n := len(run.Trail["ad.expire.kept"][gone]); n != 0 {
				t.Errorf("deleted row %s has %d ad.expire.kept records, want none", gone, n)
			}
			if n := len(run.Trail["ad.provenance.disagree"]); n != 0 {
				t.Errorf("ad.provenance.disagree written for %d rows, want none", n)
			}
			if _, err := apitest.ReadSpawnColumns(f.DBPath, gone); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("read sessionless row %s after expire: %v; want ErrSpawnNotFound", gone, err)
			}
			idtSame(t, "expire", world, idtWorld(t, f.realTmux), idtPaneIDs(world)...)
		})
	}
}

// TestExpireSocketDeniedKeepsEveryRow: with the socket at mode 000, the first row is kept
// tmux_unavailable after one refused call and the rest tmux_skipped with no further call.
func TestExpireSocketDeniedKeepsEveryRow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	bin := buildHookCLI(t, t.TempDir())
	f := newKillFix(t)
	rows := []killRow{f.liveRow(t, killRowSpec{State: "ended", NoPane: true}),
		f.liveRow(t, killRowSpec{State: "ended", NoPane: true})}
	srv := rows[0].Server
	sessionless := newInstanceID("agent")
	before := map[string]apitest.SpawnColumns{sessionless: f.seedRow(t, sessionless, uniqueName(), "ended",
		store.LaunchIdentity{Token: newToken(t), Socket: f.Socket, ServerPID: srv.PID, ServerStart: srv.Start,
			ServerStarttime: srv.Starttime})}
	for _, r := range rows {
		before[r.InstanceID] = r.Before
	}
	ids := []string{sessionless, rows[0].InstanceID, rows[1].InstanceID}
	slices.Sort(ids)
	chmodSocket(t, f.Socket, 0o000)

	run := f.expire(t, bin, true)
	chmodSocket(t, f.Socket, 0o600)
	run.assertLists(t, nil, ids)
	for i, id := range ids {
		reason := "tmux_skipped"
		if i == 0 {
			reason = "tmux_unavailable"
		}
		run.assertKept(t, before[id], id, reason)
		f.assertRowUnchanged(t, id, before[id])
	}
	if len(run.Calls) != 1 || run.Calls[0].Exit == 0 {
		t.Errorf("expire made %d tmux calls (%v); want one, refused, then no more on the socket", len(run.Calls), callArgs(run.Calls))
	}
	for _, r := range rows {
		f.assertSession(t, r.Reply.SessionID, true)
		assertProcs(t, false, r.Reply.PanePID)
	}
}
