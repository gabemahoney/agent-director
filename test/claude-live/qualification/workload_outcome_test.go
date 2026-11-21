package qualification_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// A command boundary unit: real SQLite observations, ledger and recorder;
// only external AD/Claude/tmux command execution is replaced. No model claim.
type workloadUnit struct {
	t          *testing.T
	f          *qualificationfix.Fixture
	r          *runner.Run
	e          *q.Exercise
	db, ledger string
	now        time.Time
	owner      runner.Owned
	spawn      func()
	command    func([]string, io.Writer) (bool, int, error)
}

func workloadArg(argv []string, key string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == key {
			return argv[i+1]
		}
	}
	return ""
}

func newWorkloadUnit(t *testing.T, phase string) *workloadUnit {
	t.Helper()
	f := qualificationfix.New(t, phase)
	budgets := map[string]int{"typed-controls": 6, "force-states": 5, "text-limits-backlog": 8}
	u := &workloadUnit{t: t, f: f, now: f.Manifest.Finished}
	evidence, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	u.db = filepath.Join(home, ".config/agent-director/state.db")
	if err := os.MkdirAll(filepath.Dir(u.db), 0700); err != nil {
		t.Fatal(err)
	}
	u.ledger = filepath.Join(evidence.Root, "unit-ledger.json")
	if err := evidence.JSON("unit-ledger.json", f.Ledger); err != nil {
		t.Fatal(err)
	}
	doc, caps := q.ContractInputsForTest(t)
	f.PutBytes("unit-kit/docs/caller-contract.md", doc)
	u.r = &runner.Run{Home: home, Kit: f.Path("unit-kit"), Socket: "unit-socket", Evidence: evidence, Manifest: runner.Manifest{RunID: f.Manifest.RunID, Phase: phase, TurnBudget: budgets[phase]}, Now: func() time.Time { return u.now }, Sleep: func(context.Context, time.Duration) error { return context.DeadlineExceeded }}
	u.r.Manifest.RecordingHook.Events = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolBatch", "Stop"}
	u.r.Commands = &runner.Commands{Evidence: evidence, Now: u.r.Now}
	u.r.Commands.Execute = func(_ context.Context, argv, _ []string, out, _ io.Writer) (int, error) {
		u.now = u.now.Add(time.Second)
		if len(argv) > 1 && argv[1] == "spawn" {
			iid, cwd := workloadArg(argv, "--instance-id"), workloadArg(argv, "--cwd")
			session := "unit-session-" + iid
			tr := apitest.SeedJsonlUnder(t, filepath.Join(home, ".claude"), cwd, session)
			u.owner = runner.Owned{InstanceID: iid, SessionID: session, CWD: cwd, Transcript: tr, LaunchToken: "unit-token-" + iid, Socket: u.r.Socket, Started: u.now.Add(-time.Second)}
			_, err := apitest.SeedSpawn(u.db, iid, "working", cwd, "off", session, true, apitest.WithLaunchIdentity(store.LaunchIdentity{Token: u.owner.LaunchToken, Socket: u.r.Socket}), apitest.WithLaunchStartedAt(u.owner.Started.UnixMilli()), apitest.WithJsonlPath(tr), apitest.WithNoPID())
			if err != nil {
				t.Fatal(err)
			}
			u.hook("SessionStart", runner.Row{"transcript_path": tr})
			if u.spawn != nil {
				u.spawn()
			}
		}
		if u.command != nil {
			if handled, code, err := u.command(argv, out); handled {
				return code, err
			}
		}
		if len(argv) > 1 && argv[1] == "get" {
			return 0, json.NewEncoder(out).Encode(runner.Row{"delivery_capabilities": caps})
		}
		return 0, nil
	}
	u.e = q.NewExercise(u.r, u.ledger)
	return u
}

func (u *workloadUnit) hook(event string, payload runner.Row) q.HookFact {
	u.t.Helper()
	u.now = u.now.Add(time.Millisecond)
	h := unitHook(u.t, u.f, u.owner, event, u.now, payload)
	for _, ref := range []string{h.Reference, strings.TrimSuffix(h.Reference, "input.json") + "receipt.json"} {
		if err := u.r.Evidence.Put(ref, u.f.Read(ref)); err != nil {
			u.t.Fatal(err)
		}
	}
	return h
}

func (u *workloadUnit) exec(query string, args ...any) {
	u.t.Helper()
	db, err := sql.Open("sqlite", u.db)
	if err != nil {
		u.t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(query, args...); err != nil {
		u.t.Fatal(err)
	}
}

// R14-2: the shared wait checks workload exits before its sighting predicate.
// A named ordinary F3 must not turn retained F2 sighting/order into O4 failure.
func TestQualificationForceSightingWorkloadExit(t *testing.T) {
	for _, sighted := range []bool{true, false} {
		name := "eligible-unsighted"
		if sighted {
			name = "bound-sighting-and-order"
		}
		t.Run(name, func(t *testing.T) {
			u := newWorkloadUnit(t, "force-states")
			if _, _, err := u.r.AD(context.Background(), "unit-spawn", "spawn", "--instance-id", "live-"+u.r.Manifest.RunID+"-sighting", "--cwd", t.TempDir()); err != nil {
				t.Fatal(err)
			}
			u.r.Manifest.Owned = []runner.Owned{u.owner}
			for _, id := range []string{"F2", "F3"} {
				text := "unit exact " + id
				mid := storefix.SeedBusySendResult(t, u.db, storefix.BusySendResultSeed{InstanceID: u.owner.InstanceID, Pair: store.BusySendPair{CallerUUID: u.r.Manifest.RunID, DeliveryRequestID: id}, Class: "sent_unconfirmed", Final: true, Transport: "written"})
				u.exec("UPDATE busy_send_messages SET text=?,text_bytes=?,text_sha256=? WHERE message_id=?", text, len(text), runner.Digest([]byte(text)), mid)
				if id == "F2" {
					u.exec("UPDATE busy_send_handovers SET boundary='force',forced=1,force_id='unit-F2-force',session_id=?,claimed_at_ms=?,issue_deadline_ms=?,job_deadline_ms=?,closed_at_ms=? WHERE handover_id IN (SELECT handover_id FROM busy_send_handover_members WHERE message_id=?)", u.owner.SessionID, u.now.UnixMilli(), u.now.Add(time.Minute).UnixMilli(), u.now.Add(time.Minute).UnixMilli(), u.now.UnixMilli(), mid)
					u.exec("UPDATE busy_send_prompts SET issued_at_ms=?,transport_at_ms=? WHERE handover_id IN (SELECT handover_id FROM busy_send_handover_members WHERE message_id=?)", u.now.UnixMilli(), u.now.UnixMilli(), mid)
					if sighted {
						u.exec("UPDATE busy_send_prompts SET admission_kind='prompt_sighting',admitted_at_ms=?,order_kind='ordered_acceptance',order_at_ms=? WHERE handover_id IN (SELECT handover_id FROM busy_send_handover_members WHERE message_id=?)", u.now.UnixMilli(), u.now.UnixMilli(), mid)
					}
				}
				u.r.Manifest.Cases = append(u.r.Manifest.Cases, runner.Case{ID: id, InstanceID: u.owner.InstanceID, Text: text, Mode: "wait_turn", Started: u.now})
			}
			err := q.WaitSightingForTest(context.Background(), u.e, u.owner.InstanceID, "unit exact F2")
			if err == nil || !strings.Contains(err.Error(), "ordinary-early-F3") {
				t.Fatalf("wait must retain named F3 exit: %v", err)
			}
			var workload, failed bool
			for _, row := range u.r.Manifest.Problems {
				if row.SignalID == "workload/ordinary-early-F3" {
					workload = row.Status == "not_completed"
				}
				if row.SignalID == "force-sighting" && row.Status == "fail" {
					failed = true
				}
			}
			if !workload {
				t.Error("ordinary F3 workload GAP was lost")
			}
			if failed == sighted {
				t.Errorf("force-sighting failure=%t with bound sighting/order=%t; workload exit is not the target verdict", failed, sighted)
			}
		})
	}
}

// R13-2: completed parallel calls, unrelated ordinals and background work
// cannot create the currently outstanding second foreground tool window.
func TestQualificationToolWindowCorrelation(t *testing.T) {
	for _, kind := range []string{"serial-foreground", "completed-parallel-batch", "unrelated-Bash", "background"} {
		t.Run(kind, func(t *testing.T) {
			u := newWorkloadUnit(t, "typed-controls")
			const short = "for i in 1 2 3; do sleep 1; done"
			u.spawn = func() {
				first := short
				if kind == "unrelated-Bash" {
					first = "printf unrelated"
				}
				u.hook("PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": "unit-first", "tool_input": runner.Row{"command": first}})
				if kind != "completed-parallel-batch" {
					u.hook("PostToolBatch", runner.Row{"tool_calls": []runner.Row{{"tool_use_id": "unit-first", "tool_name": "Bash"}}})
				}
				input := runner.Row{"command": short}
				if kind == "background" {
					input["run_in_background"] = true
				}
				u.hook("PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": "unit-second", "tool_input": input})
				if kind == "completed-parallel-batch" {
					u.hook("PostToolBatch", runner.Row{"tool_calls": []runner.Row{{"tool_use_id": "unit-first", "tool_name": "Bash"}, {"tool_use_id": "unit-second", "tool_name": "Bash"}}})
				}
			}
			// Reuse the real producer's binding/readiness via its initial spawn;
			// stop before any measurement input and then inspect this exact owner.
			u.command = func(argv []string, _ io.Writer) (bool, int, error) {
				if len(argv) > 1 && argv[1] == "get" {
					return false, 0, nil
				}
				return false, 0, nil
			}
			if _, _, err := u.r.AD(context.Background(), "unit-spawn", "spawn", "--instance-id", "live-"+u.r.Manifest.RunID+"-window", "--cwd", t.TempDir()); err != nil {
				t.Fatal(err)
			}
			u.r.Manifest.Owned = []runner.Owned{u.owner}
			trigger, err := q.ToolWindowForTest(context.Background(), u.e, u.owner.InstanceID, 2, false)
			if kind == "serial-foreground" {
				if err != nil || trigger == "" {
					t.Fatalf("serial foreground control: trigger=%s err=%v", trigger, err)
				}
			} else if err == nil || trigger != "" || errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s must give a named incomplete workload with no trigger/send: trigger=%q err=%v", kind, trigger, err)
			}
		})
	}
}

// R13-4: a mere Bash PreToolUse is not a harness-owned N8 hold. Also stop
// immediately on the first detected ordinary drain and charge it in finalize.
func TestQualificationBacklogHoldBeforeTraffic(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE_KZ_GATE_PROCESS") == "1" {
		if err := q.GateHook(os.Args[len(os.Args)-2], os.Args[len(os.Args)-1], os.Stdin); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, kind := range []string{"missing-tool", "wrong-unheld-tool", "foreground-no-gate", "background", "wrong-driver-PID", "wrong-driver-start", "ordinary-drain"} {
		t.Run(kind, func(t *testing.T) {
			u := newWorkloadUnit(t, "text-limits-backlog")
			sends := 0
			wrongDriver := strings.HasPrefix(kind, "wrong-driver-")
			driverChanged := false
			stop := errors.New("unit stop at forbidden backlog send")
			u.spawn = func() {
				if kind == "missing-tool" {
					return
				}
				input := runner.Row{"command": "printf no-held-driver", "run_in_background": false}
				if kind == "background" || kind == "foreground-no-gate" {
					input["command"] = q.ControlledToolForTest(filepath.Join(u.r.Evidence.Root, "work-N8", "release-never"))
				}
				if kind == "background" {
					input["run_in_background"] = true
				}
				if kind == "ordinary-drain" || wrongDriver {
					input["command"] = q.ControlledToolForTest(filepath.Join(u.r.Evidence.Root, "work-N8", "release-never"))
					u.hook("UserPromptSubmit", runner.Row{"prompt_id": "unit-N8-start", "prompt": "unit N8 startup"})
				}
				h := u.hook("PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": "unit-N8-tool", "is_background": false, "tool_input": input})
				if kind == "ordinary-drain" || wrongDriver {
					ref := u.holdGate(h)
					if wrongDriver {
						path := filepath.Join(u.r.Evidence.Root, ref, "driver.json")
						b, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						var driver runner.Row
						if err := json.Unmarshal(b, &driver); err != nil {
							t.Fatal(err)
						}
						if kind == "wrong-driver-PID" {
							driver["pid"] = os.Getpid()
						} else {
							driver["process_start"] = runner.Text(driver, "process_start") + "0"
						}
						b, err = json.Marshal(driver)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.WriteFile(path, b, 0600); err != nil {
							t.Fatal(err)
						}
						driverChanged = true
					}
				}
			}
			u.command = func(argv []string, out io.Writer) (bool, int, error) {
				if len(argv) < 2 || argv[1] != "send-keys" {
					return false, 0, nil
				}
				sends++
				if kind == "ordinary-drain" && sends == 1 {
					u.hook("Stop", nil)
					u.hook("UserPromptSubmit", runner.Row{"prompt_id": "unit-backlog-drain", "prompt": workloadArg(argv, "--text")})
					u.exec("UPDATE spawns SET turn_starts=2 WHERE instance_id=?", u.owner.InstanceID)
					return true, 0, json.NewEncoder(out).Encode(api.DeliveryResult{Class: "held"})
				}
				return true, 1, stop
			}
			err := q.LargeBacklog(context.Background(), u.e)
			if kind != "ordinary-drain" {
				b, _ := json.Marshal(u.r.Manifest.Problems)
				diagnostic := strings.ToLower(fmt.Sprint(err) + string(b))
				named := (strings.Contains(diagnostic, "backlog") || strings.Contains(diagnostic, "workload")) && (strings.Contains(diagnostic, "not") || strings.Contains(diagnostic, "hold") || strings.Contains(diagnostic, "background") || strings.Contains(diagnostic, "missing"))
				if sends != 0 || !named || errors.Is(err, stop) || errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("N8 hold absent: sends=%d err=%v; want named backlog-not-exercised and zero sends", sends, err)
				}
				if wrongDriver {
					want := "driver identity"
					if kind == "wrong-driver-start" {
						want = "driver exited"
					}
					if !driverChanged || !strings.Contains(diagnostic, want) {
						t.Errorf("wrong driver must reach exact identity guard: changed=%t err=%v", driverChanged, err)
					}
					for _, ob := range u.e.Observations {
						if ob.Kind == "backlog-hold" {
							t.Errorf("wrong process certified as hold: %+v", ob)
						}
					}
				}
				return
			}
			if sends > 1 {
				t.Errorf("ordinary drain must stop further backlog traffic immediately: sends=%d; want at most 1", sends)
			}
			for i := range u.r.Manifest.Owned {
				u.r.Manifest.Owned[i].Exited = true
				u.r.Manifest.Owned[i].StoppedAt = u.now.Add(time.Second)
			}
			// The measured branch used the injected clock. Exact child-process
			// teardown now uses the runner's bounded real IPC wait.
			u.r.Sleep = runner.Sleep
			if err := q.FinalizeExerciseForTest(context.Background(), u.e); err != nil {
				t.Fatal(err)
			}
			l, err := q.LoadLedger(u.ledger)
			if err != nil {
				t.Fatal(err)
			}
			actual, unexpected := 0, 0
			for _, c := range l.Charges {
				actual += c.Actual
				if strings.HasPrefix(c.ID, "unexpected-") {
					unexpected += c.Actual
				}
			}
			if actual != 2 || unexpected != 1 || u.r.Manifest.ObservedTurns != 2 {
				t.Errorf("ordinary backlog drain was not charged: actual=%d unexpected=%d observed=%d", actual, unexpected, u.r.Manifest.ObservedTurns)
			}
			if got := q.CheckBacklog(q.BacklogProof{}); got.Status == "pass" {
				t.Error("ordinary drain became no-drain PASS")
			}
		})
	}
}

// Run the real scheduling hook in its own process. The bounded shared wait
// observes publication of that exact passive input before admission proceeds.
func (u *workloadUnit) holdGate(h q.HookFact) string {
	u.t.Helper()
	raw, err := runner.Read(u.r.Evidence.Root, h.Reference)
	if err != nil {
		u.t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestQualificationBacklogHoldBeforeTraffic$", "--", u.r.Evidence.Root, u.r.Manifest.RunID)
	cmd.Env = append(os.Environ(), "CLAUDE_LIVE_KZ_GATE_PROCESS=1", "AGENT_DIRECTOR_INSTANCE_ID="+u.owner.InstanceID)
	cmd.Stdin = bytes.NewReader(raw)
	if err := cmd.Start(); err != nil {
		u.t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	u.t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	waiter := &runner.Run{Now: time.Now, Sleep: runner.Sleep}
	matched := ""
	err = waiter.AwaitEvidence(context.Background(), 5*time.Second, func(context.Context) (bool, error) {
		select {
		case <-done:
			return false, fmt.Errorf("gate helper exited before hold: %v", exitErr)
		default:
		}
		entries, err := os.ReadDir(filepath.Join(u.r.Evidence.Root, "raw/gates"))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			ref := "raw/gates/" + entry.Name()
			input, er := runner.Read(u.r.Evidence.Root, ref+"/input.json")
			if er != nil || !bytes.Equal(input, raw) {
				continue
			}
			var driver struct {
				PID int `json:"pid"`
			}
			data, er := runner.Read(u.r.Evidence.Root, ref+"/driver.json")
			if er == nil && json.Unmarshal(data, &driver) == nil && driver.PID == cmd.Process.Pid {
				matched = ref
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		u.t.Fatal(err)
	}
	return matched
}

// R13-1: the first fallback spends the alternative next-AD-start slot and
// closes typed; it cannot reserve an idle input as well.
func TestQualificationTypedAlternativeStartSlot(t *testing.T) {
	u := newWorkloadUnit(t, "typed-controls")
	u.spawn = func() {
		u.hook("PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": "unit-initial-long", "tool_input": runner.Row{"command": strings.Split(q.LongToolForTest, "`")[1]}})
	}
	seen, slots := false, 0
	u.command = func(argv []string, _ io.Writer) (bool, int, error) {
		if len(argv) < 2 || argv[1] != "send-keys" || !strings.HasPrefix(workloadArg(argv, "--text"), "TYPED-long-tool:") {
			return false, 0, nil
		}
		seen = true
		l, err := q.LoadLedger(u.ledger)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range l.Charges {
			if c.Phase == "typed-controls" && c.Reserved == 1 && c.ID != "typed-ad-start" && c.ID != "typed-control-start" {
				slots++
			}
		}
		saved := u.owner
		for _, o := range u.r.Manifest.Owned {
			if o.InstanceID == workloadArg(argv, "--instance-id") {
				u.owner = o
			}
		}
		u.hook("Stop", nil)
		body := string(store.RenderBusySendPromptBody([][]byte{[]byte(workloadArg(argv, "--text"))}))
		u.hook("UserPromptSubmit", runner.Row{"prompt_id": "unit-first-fallback", "prompt": "<summary>Stop hook feedback</summary>\n" + q.Transform(body, "wait_turn")})
		u.exec("UPDATE spawns SET state='waiting' WHERE instance_id=?", u.owner.InstanceID)
		u.owner = saved
		return false, 0, nil
	}
	err := q.TypedControls(context.Background(), u.e)
	if !seen {
		t.Fatalf("typed producer never reached its first busy AD input: %v", err)
	}
	if slots != 1 {
		t.Errorf("first busy steer must have one pre-reserved alternative next-AD-start slot: slots=%d; want 1", slots)
	}
	for _, c := range u.r.Manifest.Cases {
		if strings.HasPrefix(c.ID, "typed-idle-") {
			t.Errorf("typed fallback sent idle input as well: %s", c.ID)
		}
	}
	b, _ := json.Marshal(u.r.Manifest.Problems)
	diagnostic := strings.ToLower(string(b) + fmt.Sprint(err))
	if !strings.Contains(diagnostic, "stop") || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("fallback must close typed with a named outcome before idle: %s", diagnostic)
	}
}

// R13-1: preserve a real ordinary delivery after the queued group lost its
// next tool, without treating it as either steer measurement or relabelling.
func TestQualificationGroupsEarlyStopOutcome(t *testing.T) {
	f, c := unitPhysicalCapture(t, "groups", "steer_turn", true)
	o := &c.Owners[len(c.Owners)-1]
	steer, err := q.PlanGroup("steer_turn")
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := q.PlanGroup("wait_turn")
	if err != nil {
		t.Fatal(err)
	}
	f.Put("raw/group-plans.json", []q.GroupPlan{steer, ordinary})
	o.Snapshot.Tables["handovers"][0]["boundary"] = "turn_end"
	delete(o.Snapshot.Tables["handovers"][0], "force_id")
	o.Snapshot.Tables["forces"] = nil
	c.Hooks = append(c.Hooks, unitHook(t, f, o.Owner, "Stop", evidenceTime(5), nil))
	for i, p := range o.Snapshot.Tables["prompts"] {
		body, err := q.PromptBody(o.Snapshot, p)
		if err != nil {
			t.Fatal(err)
		}
		_, tr := unitJournal(t, o.Owner.SessionID, fmt.Sprintf("unit-stop-group-%d", i), "user", signals.NativeWake(q.Transform(body, "wait_turn")), evidenceTime(int64(14+i*20)))
		o.Transcript.Requests[i] = tr.Requests[0]
		if got := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "wait_turn", After: runner.Number(p, "admitted_at_ms"), Transcript: tr}); got.Status != "pass" {
			t.Fatalf("ordinary Stop input control: %+v", got)
		}
		c.Hooks = append(c.Hooks, unitHook(t, f, o.Owner, "UserPromptSubmit", evidenceTime(int64(13+i*20)), runner.Row{"prompt_id": fmt.Sprintf("unit-stop-group-%d", i), "prompt": q.Transform(body, "wait_turn")}))
	}
	row := namedResult(t, q.AnalyzeGroupsForTest(c), "steer-group")
	b, _ := json.Marshal(row)
	label := strings.ToLower(string(b))
	if row.Status != "not_completed" || !strings.Contains(label, "stop") || strings.Contains(label, "relabelled or forced") {
		t.Errorf("skipped-tool group must name early Stop and retain measurement GAP: status=%s reason=%s relabelled=%t", row.Status, row.Reason, strings.Contains(label, "relabelled or forced"))
	}
}
