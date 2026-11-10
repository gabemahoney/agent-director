package realtmux_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// A Codex agent's hooks and its kill on real tmux (E4-T3 T5; SRD SR-6.5,
// SR-6.1, SR-22.9, SR-14). The stand-in `codex` of codex_standin_test.go stays
// the pane's process and, after codexStubTail, runs the hook commands the test
// hands it (fire.<k>.*) through the account's login shell with -c, as Codex
// does, and starts a nested stand-in on request. No test here sets
// AD_CODEX_HOOK: the stand-in fires nothing on its own. The stand-in does not
// answer /exit (the Docker one does).

// codexSessionEndFixture is E4-T1's Codex SessionEnd payload (reason other).
const codexSessionEndFixture = "../../internal/hook/testdata/codex-hook-payloads/session-end-other.json"

// codexStubTail is what the stand-in runs after the shared block: the loop
// that keeps it the pane's process (never an exec) and, for each k from 1, the
// on-demand hook run of fire.<k>.cmd with fire.<k>.payload on stdin once
// fire.<k>.go exists (its stdout, stderr and exit status are fire.<k>.out,
// .err and .exit, written last), and, when the file nest appears, a nested
// stand-in started as its child with its own record directory under nested/,
// which ends when its parent does.
const codexStubTail = `shell="$(getent passwd "$(id -u)" | cut -d: -f7)"
case "${shell##*/}" in
    bash | zsh) ;;
    *) shell=bash ;;
esac
k=1
while :; do
    if [[ -n "$dir" ]]; then
        if [[ -e "$dir/nest" ]]; then
            rm -f "$dir/nest"
            AD_CODEX_RECORD_DIR="$dir/nested" AD_CODEX_NESTED=1 "$0" &
        fi
        if [[ -e "$dir/fire.$k.go" ]]; then
            "$shell" -c "$(cat "$dir/fire.$k.cmd")" <"$dir/fire.$k.payload" >"$dir/fire.$k.out" 2>"$dir/fire.$k.err"
            printf '%s\n' "$?" >"$dir/fire.$k.exit.tmp"
            mv "$dir/fire.$k.exit.tmp" "$dir/fire.$k.exit"
            k=$((k + 1))
        fi
    fi
    [[ -z "${AD_CODEX_NESTED:-}" ]] || kill -0 "$PPID" 2>/dev/null || exit 0
    sleep 0.02
done
`

// standInPane is a spawned Codex agent whose pane process is the stand-in,
// recorded and ready to run hooks.
type standInPane struct {
	*codexFix
	s       spawned
	panePID int
	rec     codexRecord
	sid     string // the session id of every payload
	rollout string // the rollout in a temporary Codex folder
	fired   int    // runs of the pane process's own hooks so far
}

// newStandInPane spawns a Codex agent whose program is the stand-in, first on
// PATH, and waits until it has recorded its launch.
func newStandInPane(t *testing.T) *standInPane {
	t.Helper()
	f := newCodexFix(t)
	unsetEnv(t, codexHookEnv)
	standIn := program(t, "codex", codexStubScript)
	onPath(t, standIn)
	s := f.spawnCodex(t)
	p := &standInPane{codexFix: f, s: s, panePID: waitPaneRuns(t, s, standIn), rec: codexRecordOf(f.root, s.ID)}
	p.rec.waitRecorded(t)
	var start struct {
		SessionID string `json:"session_id"`
	}
	raw, err := os.ReadFile(codexSessionStartFixture)
	if err != nil {
		t.Fatalf("read the Codex SessionStart fixture: %v", err)
	}
	if err := json.Unmarshal(raw, &start); err != nil || start.SessionID == "" {
		t.Fatalf("Codex SessionStart fixture: session_id %q, err %v", start.SessionID, err)
	}
	p.sid = start.SessionID
	p.rollout = codexfix.RolloutPath(t, t.TempDir(), p.sid, true)
	return p
}

// command is AD's exec-form hook command for event from the recorded launch,
// the program path swapped for the built CLI's (the spawning process is this
// test binary).
func (p *standInPane) command(t *testing.T, event string) string {
	t.Helper()
	var found []codexfix.Hook
	for _, h := range p.rec.launch(t).HooksFor(event) {
		if h.Kind == codexfix.HookAD {
			found = append(found, h)
		}
	}
	if len(found) != 1 || found[0].Agent != "codex" {
		t.Fatalf("the launch carries %d `hook --agent` handlers for %s, want one for codex", len(found), event)
	}
	quoted := "'" + found[0].Exec + "'"
	if !strings.Contains(found[0].Command, quoted) {
		t.Fatalf("%s command %q does not quote its program path %q", event, found[0].Command, quoted)
	}
	return strings.Replace(found[0].Command, quoted, "'"+p.bin+"'", 1)
}

// payload is the fixture's Codex payload with the pane's cwd, session id and
// rollout.
func (p *standInPane) payload(t *testing.T, fixture string) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read %s: %v", fixture, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	m["cwd"], m["session_id"], m["transcript_path"] = p.CWD, p.sid, p.rollout
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return out
}

// runHook has the stand-in whose record is r run its k-th hook, cmd with
// payload on stdin, and returns once it exited with status 0 and no output.
func (r codexRecord) runHook(t *testing.T, k int, cmd string, payload []byte) {
	t.Helper()
	file := func(ext string) string { return r.path(fmt.Sprintf("fire.%d.%s", k, ext)) }
	for ext, data := range map[string][]byte{"cmd": []byte(cmd), "payload": payload} {
		if err := os.WriteFile(file(ext), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", file(ext), err)
		}
	}
	if err := os.WriteFile(file("go"), nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", file("go"), err)
	}
	waitFor(t, "the stand-in's hook run "+strconv.Itoa(k)+" exits", func() bool { return r.has(filepath.Base(file("exit"))) },
		func() string { return "no " + file("exit") })
	if got := readFileTrim(t, file("exit")); got != "0" {
		t.Errorf("hook exit status %s, want 0 (stderr: %q)", got, readFileTrim(t, file("err")))
	}
	if got := readFileTrim(t, file("out")); got != "" {
		t.Errorf("hook stdout %q, want empty", got)
	}
}

// fireOwn runs the hook of event, built from fixture, from the pane process.
func (p *standInPane) fireOwn(t *testing.T, event, fixture string) {
	t.Helper()
	p.fired++
	p.rec.runHook(t, p.fired, p.command(t, event), p.payload(t, fixture))
}

// startNested has the pane process start a nested stand-in as its child, which
// inherits the instance id, and returns its record and pid.
func (p *standInPane) startNested(t *testing.T) (codexRecord, int) {
	t.Helper()
	if err := os.WriteFile(p.rec.path("nest"), nil, 0o600); err != nil {
		t.Fatalf("write nest request: %v", err)
	}
	nested := codexRecord{dir: filepath.Join(p.rec.dir, "nested", p.s.ID)}
	nested.waitRecorded(t)
	pid := nested.pid(t)
	p.s.rt.trackPane(pid)
	if id, _ := nested.env(t, "AGENT_DIRECTOR_INSTANCE_ID"); id != p.s.ID {
		t.Fatalf("the nested stand-in's AGENT_DIRECTOR_INSTANCE_ID = %q, want the row's %q", id, p.s.ID)
	}
	if got := procPPID(t, pid); got != p.panePID {
		t.Fatalf("the nested stand-in %d has parent %d, want the pane process %d", pid, got, p.panePID)
	}
	return nested, pid
}

// procPPID is the parent pid of pid.
func procPPID(t testing.TB, pid int) int {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatalf("read status of %d: %v", pid, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("PPid of %d: %v", pid, err)
			}
			return n
		}
	}
	t.Fatalf("no PPid in the status of %d", pid)
	return 0
}

// TestCodexNestedHookIsIgnored: a stand-in Codex started inside the AD-launched
// Codex's pane inherits the instance id, and its hook, run through the login
// shell with -c and AD's exec-form entry, has the nested Codex as its parent: the
// row is untouched and one ad.hook.ignored with pid_mismatch is written. The
// gate's rule for ad.hook.pane_is_grandparent is a SessionStart refused on a
// pending row whose pane process is the parent's parent: the nested Codex's
// SessionStart on the pending row writes one, naming codex; on a waiting row
// none.
func TestCodexNestedHookIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name    string
		waiting bool // the pane process's own SessionStart applied first
	}{
		{name: "pending row, one grandparent record"},
		{name: "waiting row, no grandparent record", waiting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newStandInPane(t)
			if tc.waiting {
				p.fireOwn(t, "SessionStart", codexSessionStartFixture)
				if row := readRow(t, p.DBPath, p.s.ID); row.State != "waiting" || row.SessionID != p.sid {
					t.Fatalf("row after the pane process's own SessionStart: state %v, session %v; want waiting, %s", row.State, row.SessionID, p.sid)
				}
			}
			before := readRow(t, p.DBPath, p.s.ID)
			nested, nestedPID := p.startNested(t)

			nested.runHook(t, 1, p.command(t, "SessionStart"), p.payload(t, codexSessionStartFixture))

			if after := readRow(t, p.DBPath, p.s.ID); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed by a nested Codex's hook:\n  before %+v\n  after  %+v", before, after)
			}
			ignored := hookTrailRecords(t, p.home, "ad.hook.ignored", p.s.ID)
			detected := hookTrailRecords(t, p.home, "ad.hook.pane_is_grandparent", p.s.ID)
			if !tc.waiting {
				p.assertIgnoredAndGrandparent(t, p.s.ID, p.panePID, p.sid, ignored, detected)
			} else {
				if len(ignored) != 1 {
					t.Fatalf("%d ad.hook.ignored records, want exactly one", len(ignored))
				}
				for k, w := range map[string]string{
					"hook_event":     "SessionStart",
					"reason":         "pid_mismatch",
					"parent_command": "codex",
					"row_pane_pid":   strconv.Itoa(p.panePID),
					"source":         "ad_hook",
				} {
					if got := fmt.Sprint(ignored[0][k]); got != w {
						t.Errorf("ad.hook.ignored %s = %s, want %s", k, got, w)
					}
				}
				if len(detected) != 0 {
					t.Errorf("%d ad.hook.pane_is_grandparent records for a waiting row, want none (the gate's rule is a pending row's SessionStart)", len(detected))
				}
			}
			if got := fmt.Sprint(ignored[0]["parent_pid"]); got != strconv.Itoa(nestedPID) {
				t.Errorf("ad.hook.ignored parent_pid = %s, want the nested Codex %d", got, nestedPID)
			}
		})
	}
}

// TestCodexKillLeavesTheRowAsItWas: kill of a waiting Codex row, without and
// with an end report (a SessionEnd, reason other, run through -c from the
// pane process), ends the pane and the labelled session, returns once the agent
// process is gone and writes nothing: the whole row, end_reported_at included,
// is as it was (SR-6.1, SR-6.5).
func TestCodexKillLeavesTheRowAsItWas(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report bool
	}{
		{name: "no end report"},
		{name: "with an end report", report: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newStandInPane(t)
			p.fireOwn(t, "SessionStart", codexSessionStartFixture)
			if tc.report {
				p.fireOwn(t, "SessionEnd", codexSessionEndFixture)
			}
			before := readRow(t, p.DBPath, p.s.ID)
			if before.State != "waiting" || (before.EndReportedAt != nil) != tc.report {
				t.Fatalf("row before the kill: state %v, end_reported_at %v; want waiting with the end report %v", before.State, before.EndReportedAt, tc.report)
			}

			res, err := p.API.Kill(api.KillParams{InstanceID: p.s.ID})
			if err != nil || !res.KillSent {
				t.Fatalf("kill: %+v, %v; want success with kill_sent true", res, err)
			}
			// Kill returns only once the agent process is gone.
			assertProcs(t, true, p.panePID)
			p.s.rt.assertSession(t, p.s.SessionID, false)
			if after := readRow(t, p.DBPath, p.s.ID); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed by kill:\n  before %+v\n  after  %+v", before, after)
			}
		})
	}
}

// b.x9o, doc-review R7: literally follow permissions.md's held-spawn exit
// procedure. Each polling verb gets a fresh launch; no state read, sweep or
// pause can reconcile the exit before that verb. Reuse the Docker stand-in's
// real /exit hook, the Codex launch fixture and the public CLI caller.
func TestAdviceFollowCodexHeldExitReadPolling(t *testing.T) {
	doc, err := os.ReadFile("../../docs/permissions.md")
	if err != nil {
		t.Fatal(err)
	}
	advice := "agent-director send-keys --instance-id <id> --text /exit \\\n       --expect-pane-sha256 <H>"
	if !strings.Contains(string(doc), advice) || !strings.Contains(string(doc), "Poll `get` or `status` until the row reads `ended`") {
		t.Fatal("held-spawn exit advice changed; recheck its literal follow")
	}
	for _, poll := range []string{"get", "status"} {
		t.Run(poll, func(t *testing.T) {
			f := newCodexFix(t)
			script, err := os.ReadFile(codexDriverStandIn)
			if err != nil {
				t.Fatal(err)
			}
			const prompt = "codex-exit>"
			// Set the empty prompt immediately before the interactive shell;
			// the noninteractive stand-in discards an inherited PS1.
			tail := "exec bash --noprofile --norc -i"
			if strings.Count(string(script), tail) != 1 {
				t.Fatal("stand-in shell tail changed")
			}
			script = []byte(strings.Replace(string(script), tail, "export PS1='"+prompt+" '\n"+tail, 1))
			onPath(t, program(t, "codex", string(script)))
			onPath(t, f.bin) // the stand-in's SessionEnd uses this built CLI
			s := f.mustLaunchWith(t, api.SpawnParams{InstanceID: newInstanceID("cx-exit"), Agent: "codex", RelayMode: "on"})
			h := f.fire(t, s.ID, f.CWD)
			h.assertHookRan(t)
			if err := os.WriteFile(h.Rec.path("session-id"), []byte(h.SessionID), 0o600); err != nil {
				t.Fatal(err)
			}
			pid := h.Rec.pid(t)
			// A delivered request still holds the dialog until native proof. It
			// needs no fallen-back answer, so step 1 can inspect the empty prompt.
			request, err := apitest.SeedPermissionRequest(f.DBPath, s.ID, "Bash",
				apitest.PermissionDecision("allow", ""), apitest.PermissionDelivered(time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			pr, err := st.GetPermissionRequest(s.ID, request.RequestToken)
			if err != nil || pr.AwaitsAnswer() || pr.ProvenGone() {
				t.Fatalf("request must be answered but unproven: %+v, %v", pr, err)
			}
			cli := newPaneCaller(t, f.spawnFix, "CLI", f.bin)
			plain := map[string]any{"instance_id": s.ID, "text": "/exit"}
			raw, err := cli.call("send-keys", plain)
			var refusal struct {
				Name string `json:"err_name"`
			}
			if err == nil || json.Unmarshal(raw, &refusal) != nil || refusal.Name != "ErrDialogMaybeOpen" {
				t.Fatalf("plain exit must be held: %s, %v", raw, err)
			}
			t.Logf("plain send-keys /exit: %s", raw)
			read := func() api.ReadPaneResult {
				t.Helper()
				raw := cli.must("read-pane", map[string]any{"instance_id": s.ID})
				var pane api.ReadPaneResult
				if err := json.Unmarshal(raw, &pane); err != nil {
					t.Fatal(err)
				}
				return pane
			}
			// Step 1: inspect the pane. Only read-pane waits for the shell's
			// first empty prompt; no unrelated read supplies an observation.
			var first api.ReadPaneResult
			waitFor(t, "empty stand-in prompt", func() bool {
				first = read()
				return strings.TrimSpace(first.Pane) == prompt
			}, func() string { return fmt.Sprintf("pane %q", first.Pane) })
			t.Logf("step 1 read-pane: %+v", first)
			// Step 2: read again, inspect the exact empty prompt and use only
			// this read's hash. No draft is cleared or silently submitted.
			second := read()
			if strings.TrimSpace(second.Pane) != prompt || second.PaneSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(second.Pane))) {
				t.Fatalf("second read is not the inspected empty prompt: %+v", second)
			}
			t.Logf("step 2 read-pane: %+v", second)
			before := readRow(t, f.DBPath, s.ID)
			if before.State != "waiting" || before.EndReportedAt != nil || before.EndedAt != nil {
				t.Fatalf("before exit: %+v", before)
			}
			// Step 3: the documented command, through the real CLI adapter.
			plain["expect_pane_sha256"] = second.PaneSHA256
			t.Logf("step 3 send-keys /exit --expect-pane-sha256 %s: %s", second.PaneSHA256, cli.must("send-keys", plain))
			waitFor(t, "SessionEnd hook completes", func() bool { return h.Rec.has("exit-hook.exit") }, nil)
			if got := readFileTrim(t, h.Rec.path("exit-hook.exit")); got != "0" {
				t.Fatalf("SessionEnd exit %q: %s", got, readFileTrim(t, h.Rec.path("exit-hook.err")))
			}
			waitPidGone(t, pid)
			unreconciled := readRow(t, f.DBPath, s.ID)
			if unreconciled.State != "waiting" || unreconciled.EndedAt != nil || unreconciled.EndReportedAt == nil {
				t.Fatalf("only the end report should be stored before polling: %+v", unreconciled)
			}
			t.Logf("process gone; direct store before %s: state=%v end_reported_at=%v ended_at=%v", poll,
				unreconciled.State, unreconciled.EndReportedAt, unreconciled.EndedAt)
			// Step 4: only this documented verb can perform reconciliation.
			var response struct {
				State string `json:"state"`
			}
			waitFor(t, poll+" reconciles ended", func() bool {
				raw = cli.must(poll, map[string]any{"instance_id": s.ID})
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				return response.State == "ended"
			}, func() string { return string(raw) })
			after := readRow(t, f.DBPath, s.ID)
			if after.State != "ended" || after.EndedAt == nil || after.EndReportedAt != unreconciled.EndReportedAt ||
				after.PID != before.PID || after.ProcStarttime != before.ProcStarttime || after.SessionID != before.SessionID {
				t.Fatalf("durable ended result lost exit/launch identity: %+v", after)
			}
			t.Logf("step 4 %s: %s; durable state=%v ended_at=%v end_reported_at=%v", poll, raw,
				after.State, after.EndedAt, after.EndReportedAt)
		})
	}
}
