package runner_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// t3.xis.w3.ci.ai R2-F1: execute Probe itself, ending before live qualification.
func TestClaudeLiveProbeScheduling(t *testing.T) {
	// Serial: the existing isolated runner fixture changes HOME and PATH.
	kit := runner.LifecycleKitForSignalsTest(t)
	boundary := errors.New("stop at Plain scheduling boundary")
	for _, tc := range []struct {
		name, reason string
		trace        []string
	}{
		{"plain-while-held", "", []string{"spawn", "send:overlap-wake", "force", "send:overlap-steer", "send:idle-wake"}},
		{"text-window-ended", "text-only window already ended; no automatic retry", []string{"spawn"}},
		{"wake-already-sighted", "forced wake already sighted or ordered; overlap window missed, no retry", []string{"spawn", "send:overlap-wake", "force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := signalsfix.Copy(t)
			r := runner.ScopedRunnerForSignalsTest(t, t.TempDir(), kit, "probe-schedule")
			r.Manifest.RunID, r.Manifest.TurnBudget = f.Manifest.RunID, 7
			clock := tmuxfix.NewClock(f.Manifest.Started)
			r.Now, r.Commands.Now = clock.Now, clock.Now
			t.Cleanup(func() {
				if r.Settings != nil {
					r.Settings.Close()
				}
			})

			// LookPath is real; all commands below are intercepted, never executed.
			tools := t.TempDir()
			for _, name := range []string{"claude", "tmux", "sqlite3", "bash", "cp", "jq"} {
				p := filepath.Join(tools, name)
				runner.PutFixtureForSignalsTest(t, p, []byte("#!/bin/sh\nexit 99\n"))
				if err := os.Chmod(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			var identity runner.Kit
			b, err := runner.Read(kit, "kit.json")
			if err != nil || json.Unmarshal(b, &identity) != nil {
				t.Fatalf("kit identity: %v", err)
			}
			for _, component := range []string{"agent-director", "agent-director-admin"} {
				b, err := runner.Read(kit, "payload/"+component+"-linux-"+runtime.GOARCH)
				if err != nil {
					t.Fatal(err)
				}
				dir := "bin"
				if component == "agent-director-admin" {
					dir = "admin"
				}
				runner.PutFixtureForSignalsTest(t, filepath.Join(r.Home, ".config/agent-director", dir, component), b)
			}

			work := filepath.Join(r.Evidence.Root, "work")
			transcript := apitest.SeedJsonlUnder(t, filepath.Join(r.Home, ".claude"), work, f.Manifest.Owned[0].SessionID)
			var window struct {
				Observation runner.Snapshot `json:"observation"`
			}
			f.Decode("raw/text-finish-window-0.json", &window)
			var pending, held, final runner.Snapshot
			f.Decode("raw/pending-wake-store.json", &pending)
			f.Decode("raw/overlap-held-store.json", &held)
			f.Decode(f.Store(0), &final)
			// The captured Held read follows Plain's acceptance. Before its command,
			// replay the same custody facts with that not-yet-accepted Message absent.
			var messages []runner.Row
			for _, m := range held.Tables["messages"] {
				if runner.Text(m, "text") != signals.Plain {
					messages = append(messages, m)
				}
			}
			held.Tables["messages"] = messages
			if tc.name == "wake-already-sighted" {
				// A delayed observation sees the real complete sighting/order pair.
				f.Decode("raw/order-observations-0/0004.json", &pending)
			}
			publish := probeSnapshotReplay(t, r, transcript, final)
			if err := publish(window.Observation); err != nil {
				t.Fatal(err)
			}
			probeCapturedInput(t, f, r, work, transcript, tc.name == "text-window-ended")

			var mu sync.Mutex
			var trace []string
			forceStarted := make(chan struct{})
			var forceOnce sync.Once
			startupSleeps := 0
			r.Sleep = func(ctx context.Context, d time.Duration) error {
				mu.Lock()
				defer mu.Unlock()
				if reflect.DeepEqual(trace, []string{"spawn"}) && startupSleeps == 0 {
					// waitTool first binds the owner, then sees its captured receipt.
					startupSleeps++
					clock.Advance(d)
					return ctx.Err()
				}
				return fmt.Errorf("unexpected probe wait after %v", trace)
			}
			r.Commands.Execute = func(ctx context.Context, argv, _ []string, out, _ io.Writer) (int, error) {
				write := func(v any) (int, error) { return 0, json.NewEncoder(out).Encode(v) }
				if argv[0] == "claude" {
					if len(argv) > 1 && argv[1] == "auth" {
						return write(map[string]any{"loggedIn": true, "subscriptionType": "synthetic-command-seam"})
					}
					_, err := io.WriteString(out, f.Manifest.ProviderVersion+"\n")
					return 0, err
				}
				if len(argv) > 1 && argv[1] == "version" {
					return write(map[string]string{"version": identity.Version, "commit": identity.CommitStamp})
				}
				op := argv[1]
				if op == "send-keys" {
					op = "send:" + probeFlag(argv, "--delivery-request-id")
				}
				if op == "force-delivery" {
					op = "force"
				}
				if op == "send:overlap-steer" {
					select {
					case <-forceStarted:
					case <-ctx.Done():
						return 1, ctx.Err()
					}
				}
				mu.Lock()
				trace = append(trace, op)
				mu.Unlock()
				switch op {
				case "spawn":
					return write(map[string]string{"instance_id": f.Manifest.Owned[0].InstanceID})
				case "force":
					forceOnce.Do(func() { close(forceStarted) })
					return write(map[string]any{"force_result": map[string]string{"outcome": "pending"}})
				case "send:overlap-wake":
					if probeFlag(argv, "--text") != signals.WakeOrder || probeFlag(argv, "--send-mode") != "wait_turn" {
						return 1, errors.New("wrong wake input")
					}
					return 0, publish(pending)
				case "send:overlap-steer":
					if probeFlag(argv, "--text") != signals.OverlapSteer || probeFlag(argv, "--send-mode") != "steer_turn" {
						return 1, errors.New("wrong steer input")
					}
					return 0, publish(held)
				case "send:idle-wake":
					if probeFlag(argv, "--text") != signals.Plain || probeFlag(argv, "--send-mode") != "wait_turn" {
						return 1, errors.New("Plain was not scheduled in Wait Turn mode")
					}
					s, err := r.Observe(ctx, f.Manifest.Owned[0].InstanceID)
					if err != nil {
						return 1, err
					}
					for _, m := range s.Tables["messages"] {
						if runner.Text(m, "text") == signals.OverlapSteer && runner.Text(m, "place") == "held" {
							return 1, boundary
						}
					}
					return 1, errors.New("STEER was not Held when Plain was scheduled")
				default:
					return 1, fmt.Errorf("unexpected probe command: %v", argv)
				}
			}
			err = signals.Probe(context.Background(), r)
			mu.Lock()
			gotTrace := append([]string(nil), trace...)
			mu.Unlock()
			if !reflect.DeepEqual(gotTrace, tc.trace) {
				t.Fatalf("probe scheduling/retry trace = %v; want %v; result: %v", gotTrace, tc.trace, err)
			}
			if tc.reason == "" {
				if !errors.Is(err, boundary) {
					t.Fatalf("Probe did not schedule Plain during Held: %v", err)
				}
			} else if err == nil || err.Error() != tc.reason {
				t.Fatalf("missed window must stop without retry: got %v; want %q", err, tc.reason)
			}
		})
	}
}

func probeFlag(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// Replay collector rows through its real read-only SQLite boundary. These views
// project captured inputs; they are not an AD schema and no product writer runs.
func probeSnapshotReplay(t *testing.T, r *runner.Run, transcript string, shape runner.Snapshot) func(runner.Snapshot) error {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(r.Home, ".config/agent-director/state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE probe_capture(body TEXT); INSERT INTO probe_capture VALUES ('{}')"); err != nil {
		t.Fatal(err)
	}
	for name, table := range map[string]string{"spawns": "spawns", "launches": "busy_send_launches", "messages": "busy_send_messages", "handovers": "busy_send_handovers", "prompts": "busy_send_prompts", "members": "busy_send_handover_members", "forces": "busy_send_forces"} {
		if len(shape.Tables[name]) == 0 {
			t.Fatalf("captured shape has no %s rows", name)
		}
		var columns []string
		for key := range shape.Tables[name][0] {
			columns = append(columns, fmt.Sprintf("json_extract(value,'$.%s') AS %q", key, key))
		}
		sort.Strings(columns)
		if name == "handovers" {
			// Collector output is already ordered by the unexported handover_seq.
			columns = append(columns, "CAST(key AS INTEGER) AS handover_seq")
		}
		query := fmt.Sprintf("CREATE VIEW %s AS SELECT %s FROM probe_capture,json_each(body,'$.tables.%s')", table, strings.Join(columns, ","), name)
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return func(s runner.Snapshot) error {
		for _, row := range s.Tables["spawns"] {
			row["jsonl_path"], row["tmux_socket"] = transcript, r.Socket
		}
		b, err := json.Marshal(s)
		if err == nil {
			_, err = db.Exec("UPDATE probe_capture SET body=?", string(b))
		}
		return err
	}
}

func probeCapturedInput(t *testing.T, f *signalsfix.Fixture, r *runner.Run, work, path string, ended bool) {
	t.Helper()
	data := f.Read(f.Transcript(0))
	parsed, err := signals.ParseTranscript(data, f.Manifest.Owned[0].SessionID)
	if err != nil || len(parsed.Requests) < 3 {
		t.Fatalf("captured request window: %v", err)
	}
	last := parsed.Requests[1].Line
	if ended {
		last = 0
		for _, row := range parsed.Rows {
			if row.Type == "assistant" && row.RequestRef == parsed.Requests[1].ID && row.Message.StopReason == "end_turn" {
				last = row.Line
				break
			}
		}
		if last == 0 {
			t.Fatal("captured text-only terminal missing")
		}
	}
	runner.PutFixtureForSignalsTest(t, path, append(bytes.Join(bytes.Split(data, []byte("\n"))[:last], []byte("\n")), '\n'))
	var first runner.Receipt
	var input runner.Row
	for name := range f.Manifest.Artifacts {
		if !strings.HasPrefix(name, "raw/hooks/") || !strings.HasSuffix(name, "/receipt.json") {
			continue
		}
		var rec runner.Receipt
		f.Decode(name, &rec)
		var row runner.Row
		f.Decode(filepath.ToSlash(filepath.Join(filepath.Dir(name), rec.Input)), &row)
		if rec.Event == "PreToolUse" && runner.Text(row, "session_id") == f.Manifest.Owned[0].SessionID && (first.Received.IsZero() || rec.Received.Before(first.Received)) {
			first, input = rec, row
		}
	}
	if input == nil {
		t.Fatal("captured parent tool receipt missing")
	}
	input["cwd"], input["transcript_path"] = work, path
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	first.SHA256, first.Bytes = runner.Digest(b), len(b)
	if err := r.Evidence.Put("raw/hooks/invocation-probe/input.json", b); err != nil {
		t.Fatal(err)
	}
	if err := r.Evidence.JSON("raw/hooks/invocation-probe/receipt.json", first); err != nil {
		t.Fatal(err)
	}
}
