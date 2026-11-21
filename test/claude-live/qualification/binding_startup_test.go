package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/bindingfix"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// R11-1/2: actual paired producer -> checked ready wait -> ReadTranscript.
// The final a4 manifest is provenance, not an initial-ready snapshot. Session
// publication/observation below is explicit unit scheduling on real SQLite.
func TestQualificationRecordedStartupBinding(t *testing.T) {
	for _, scenario := range []string{"a4-checked-first", "a4-sibling-first", "synthetic-reverse-checked-first", "synthetic-reverse-sibling-first", "sibling-later", "never-sibling"} {
		t.Run(scenario, func(t *testing.T) {
			var clip struct {
				Owned []runner.Owned
				Hooks []struct {
					Receipt runner.Receipt
					Input   runner.Row
				}
			}
			data, err := os.ReadFile("../runner/testdata/recorded/a4-transcript-binding.json")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &clip); err != nil {
				t.Fatal(err)
			}
			f := qualificationfix.New(t, "typed-controls")
			evidence, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			dbPath := filepath.Join(home, ".config/agent-director/state.db")
			if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := apitest.InitStore(dbPath); err != nil {
				t.Fatal(err)
			}
			// Transcript collection must still retain the independent scoped trail.
			if err := os.WriteFile(filepath.Join(home, ".config/agent-director/ad-trail.jsonl"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ledger := filepath.Join(t.TempDir(), "ledger.json")
			ledgerBytes, err := json.Marshal(f.Ledger)
			if err != nil || os.WriteFile(ledger, ledgerBytes, 0600) != nil {
				t.Fatal("cannot write unit ledger", err)
			}
			now := time.Now().UTC()
			base := now
			r := &runner.Run{Home: home, Evidence: evidence, Socket: "unit-paired-binding", Now: func() time.Time { return now },
				Manifest: runner.Manifest{RunID: f.Manifest.RunID, Phase: "typed-controls", TurnBudget: 6, Started: base, Deadline: base.Add(runner.PhaseLimit)}}
			r.Manifest.RecordingHook = runner.RecordingHook{Installed: true, Directory: "raw/hooks", Events: []string{"SessionStart", "UserPromptSubmit"}}
			if err := os.MkdirAll(filepath.Join(evidence.Root, "raw/hooks"), 0700); err != nil {
				t.Fatal(err)
			}
			var parents []runner.Owned
			stops, kills := map[string]func(){}, map[string]int{}
			published := [2]bool{}
			receiptPublished := false
			publish := func(i int) {
				t.Helper()
				if published[i] {
					return
				}
				o := parents[i]
				applied, err := storefix.FireHook(db, o.InstanceID, "SessionStart", o.SessionID, o.PID, o.ProcessStart, storefix.HookTranscript(o.Transcript, true))
				if err != nil || !applied.Applied {
					t.Fatalf("unit product SessionStart %d: %+v %v", i, applied, err)
				}
				published[i] = true
			}
			writeHook := func(index int, position int) {
				t.Helper()
				h := clip.Hooks[index]
				i := 0
				if runner.Text(h.Input, "session_id") == clip.Owned[1].SessionID {
					i = 1
				}
				o := parents[i]
				input, err := json.Marshal(runner.Row{"hook_event_name": h.Receipt.Event, "session_id": o.SessionID, "transcript_path": o.Transcript, "cwd": o.CWD})
				if err != nil {
					t.Fatal(err)
				}
				receipt := h.Receipt
				receipt.RunID, receipt.Bytes, receipt.SHA256 = r.Manifest.RunID, len(input), runner.Digest(input)
				receipt.Received = base.Add(h.Receipt.Received.Sub(clip.Hooks[0].Receipt.Received))
				receipt.Finished = base.Add(h.Receipt.Finished.Sub(clip.Hooks[0].Receipt.Received))
				if strings.HasPrefix(scenario, "synthetic-reverse") && index < 2 {
					receipt.Received, receipt.Finished = base.Add(time.Duration(position)*time.Millisecond), base.Add(time.Duration(position)*time.Millisecond+time.Microsecond)
				}
				meta, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				for name, b := range map[string][]byte{"input.json": input, "receipt.json": meta} {
					if err := evidence.Put(fmt.Sprintf("raw/hooks/invocation-replay-%d/%s", position, name), b); err != nil {
						t.Fatal(err)
					}
				}
				if index == 0 {
					receiptPublished = true
				}
			}
			cleaning, reached, waits := false, false, 0
			stop := errors.New("unit checkpoint after both checked ready waits; before capability/input")
			r.Sleep = func(ctx context.Context, d time.Duration) error {
				now = now.Add(d)
				if cleaning {
					return ctx.Err()
				}
				waits++
				if waits > 3 || scenario == "never-sibling" {
					return context.DeadlineExceeded
				}
				if scenario == "sibling-later" {
					publish(1)
				}
				return ctx.Err()
			}
			r.Commands = &runner.Commands{Evidence: evidence, Now: r.Now, Execute: func(_ context.Context, argv, _ []string, _, _ io.Writer) (int, error) {
				arg := func(key string) string {
					for i := 0; i+1 < len(argv); i++ {
						if argv[i] == key {
							return argv[i+1]
						}
					}
					return ""
				}
				id := arg("--instance-id")
				switch argv[1] {
				case "spawn":
					i := len(parents)
					pid, start, halt := bindingfix.Child(t)
					o := runner.Owned{InstanceID: id, CWD: arg("--cwd"), Socket: r.Socket, SessionID: clip.Owned[i].SessionID,
						LaunchToken: clip.Owned[i].LaunchToken, PID: pid, ProcessStart: start, Started: now}
					o.Transcript = apitest.SeedJsonlUnder(t, filepath.Join(home, ".claude"), o.CWD, o.SessionID)
					if err := os.WriteFile(o.Transcript, nil, 0600); err != nil {
						t.Fatal(err)
					}
					_, err := apitest.SeedSpawn(dbPath, id, "pending", o.CWD, "off", "", false,
						apitest.WithLaunchIdentity(store.LaunchIdentity{Token: o.LaunchToken, Socket: o.Socket, PaneID: "%1", PanePID: pid, PaneStarttime: start}),
						apitest.WithPID(pid), apitest.WithProcStarttime(start), apitest.WithLaunchStartedAt(now.UnixMilli()))
					if err != nil {
						t.Fatal(err)
					}
					parents, stops[id] = append(parents, o), halt
					if len(parents) == 2 {
						publish(0)
						if scenario != "sibling-later" && scenario != "never-sibling" {
							publish(1)
						}
						order := []int{0, 1, 2}
						if strings.HasPrefix(scenario, "synthetic-reverse") {
							order = []int{1, 0, 2}
						}
						for position, index := range order {
							writeHook(index, position)
						}
						if strings.HasSuffix(scenario, "sibling-first") {
							snapshot, err := r.Observe(context.Background(), parents[1].InstanceID)
							if err != nil || r.BindOwned(snapshot) != nil {
								t.Fatal("cannot observe sibling first", err)
							}
						}
					}
				case "get":
					if !cleaning {
						reached = true
						if !receiptPublished || !published[1] || r.Manifest.Owned[1].SessionID == "" {
							t.Error("startup accepted the pair before recorder/independent sibling binding")
						}
						return 1, stop
					}
				case "read-pane":
					if !cleaning {
						t.Error("unexpected pane read during startup")
					}
				case "kill":
					if !cleaning || stops[id] == nil {
						t.Fatal("unowned/non-cleanup kill")
					}
					kills[id]++
					stops[id]()
				default:
					t.Fatalf("startup crossed its input checkpoint: %q", argv)
				}
				return 0, nil
			}}
			defer func() {
				cleaning = true
				cleanupErr := r.Cleanup(context.Background())
				for _, parent := range parents {
					gone, err := runner.ProcessGone(parent.PID, parent.ProcessStart)
					if kills[parent.InstanceID] == 0 || !gone || err != nil {
						t.Errorf("failure suppressed canonical owned cleanup: %s kills=%d gone=%v error=%v", parent.InstanceID, kills[parent.InstanceID], gone, err)
					}
				}
				if scenario == "never-sibling" {
					if cleanupErr == nil {
						t.Error("never-bound sibling produced a clean final transcript collection")
					}
				} else if cleanupErr != nil {
					t.Errorf("bound pair cleanup must retain both transcripts: %v", cleanupErr)
				}
			}()
			err = q.TypedControls(context.Background(), q.NewExercise(r, ledger))
			if scenario == "never-sibling" {
				if !errors.Is(err, context.DeadlineExceeded) || reached || waits == 0 {
					t.Errorf("never-bound sibling must remain incomplete at bounded timeout, without input: reached=%v waits=%d error=%v", reached, waits, err)
				}
			} else if !errors.Is(err, stop) || !reached {
				t.Errorf("paired startup must reach both ready waits before input: checkpoint=%v waits=%d error=%v", reached, waits, err)
			}
			if scenario == "sibling-later" && waits == 0 {
				t.Error("late recorder/identity never reached its causal wait")
			}
		})
	}
}
