package api_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func seedContext(t *testing.T, sample contextfix.Sample, extra ...apitest.SpawnOption) (*store.Store, string, string) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "state.db")
	id, path := apitest.SeedContextSpawn(t, db, "", true, sample, extra...)
	return openDB(t, db), id, path
}

func TestContextRequiresCurrentFreshWriter(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"creation metadata only", "untested resumed runtime retaining tested creation metadata", "ended", "end report", "dead writer"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sample := contextfix.Must(t, "writer")
			var opts []apitest.SpawnOption
			var process *exec.Cmd
			if name == "dead writer" {
				process = exec.Command("sleep", "60")
				if err := process.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
				identity, err := apitest.ContextWriter(process.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				opts = append(opts, apitest.WithLaunchIdentity(identity))
			}
			var s *store.Store
			var id, path string
			if name == "creation metadata only" {
				db := filepath.Join(t.TempDir(), "state.db")
				path = apitest.SeedContextTranscript(t, sample)
				identity, err := apitest.ContextWriter(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				id, err = apitest.SeedSpawn(db, "", store.StateWaiting, t.TempDir(), "off", sample.SessionID, true, apitest.WithAgent(store.AgentCodex), apitest.WithJsonlPath(path), apitest.WithLaunchIdentity(identity), apitest.WithPID(identity.PanePID), apitest.WithProcStarttime(identity.PaneStarttime))
				if err != nil {
					t.Fatal(err)
				}
				s = openDB(t, db)
			} else {
				s, id, path = seedContext(t, sample, opts...)
				baseline, err := api.Status(s, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, baseline.Context, sample.Expected)
				sp, err := s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				g := store.HookGate{Event: "SessionStart", SessionStart: true, Examined: sp.Snapshot, SessionID: sp.SessionID, ParentPID: sp.PID, ParentStart: sp.ProcStarttime}
				switch name {
				case "untested resumed runtime retaining tested creation metadata":
					a, changed, err := s.RecordSessionStartIdentity(id, g, path, true)
					if err != nil || !a.Applied || changed {
						t.Fatalf("resume admission: %+v %v %v", a, changed, err)
					}
				case "ended":
					g.Event = "SessionEnd"
					if a, err := s.ApplyHookTransition(id, g, store.StateEnded, false, "SessionEnd", "", false); err != nil || !a.Applied {
						t.Fatalf("end: %+v %v", a, err)
					}
				case "end report":
					g.Event = "SessionEnd"
					if _, a, err := s.ApplyHookEndReport(id, g, "SessionEnd", "", false); err != nil || !a.Applied {
						t.Fatalf("end report: %+v %v", a, err)
					}
				case "dead writer":
					if err := process.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					_ = process.Wait()
				}
			}
			for _, view := range []interface {
				api.StatusStore
				api.GetStore
			}{s, s.WithCursorMissingReads(nil)} {
				status, err := api.Status(view, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, status.Context, json.RawMessage(contextfix.UnknownJSON))
				row, err := api.Get(view, api.RelayView{}, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, row.Context, json.RawMessage(contextfix.UnknownJSON))
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, sample.Transcript) {
				t.Fatal("test must retain the original 0.159.2 creation metadata and usage")
			}
		})
	}
}

func assertContext(t *testing.T, got api.ContextReading, want json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected map[string]any
	if err = json.Unmarshal(b, &actual); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("context = %s; want %s", b, want)
	}
}

// Native capture ordering and synthetic attribution/accounting variants belong here.
func TestContextReaders(t *testing.T) {
	t.Parallel()
	samples := []contextfix.Sample{}
	for _, name := range []string{"writer", "reviewer", "claude-parent", "claude-missing-capacity", "cursor-2026.10.01-e373342", "cursor-2026.10.12-d47ca18"} {
		samples = append(samples, contextfix.Must(t, name))
	}
	for _, name := range []string{"untested-version", "wrong-provider", "missing-provider", "wrong-history", "fork", "history-base", "wrong-session", "missing-session", "missing-usage", "missing-current", "missing-capacity", "zero-capacity", "wrong-capacity", "negative-usage", "missing-total", "missing-time", "regressing-time", "wrong-turn", "missing-model", "missing-start-window", "compaction-gap", "response-gap", "unknown-event", "post-compaction", "zero-usage"} {
		samples = append(samples, contextfix.Variant(t, name))
	}
	for _, sample := range samples {
		t.Run(sample.Name, func(t *testing.T) {
			t.Parallel()
			s, id, _ := seedContext(t, sample)
			got, err := api.Status(s, id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, got.Context, sample.Expected)
		})
	}
}

func TestContextUnreadableEvidenceKeepsReadSuccessful(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"relative-path", "denied", "missing", "directory", "fifo", "partial-json", "unterminated-record", "malformed-tail", "overlong-line", "oversized-file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sample := contextfix.Must(t, "writer")
			var opts []apitest.SpawnOption
			if name == "relative-path" {
				opts = append(opts, apitest.WithJsonlPath("relative.jsonl"))
			}
			s, id, path := seedContext(t, sample, opts...)
			switch name {
			case "denied":
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "partial-json":
				sample.Transcript = append(sample.Transcript, []byte("{\"type\":")...)
			case "unterminated-record":
				sample.Transcript = bytes.TrimSuffix(sample.Transcript, []byte("\n"))
			case "malformed-tail":
				sample.Transcript = append(sample.Transcript, []byte("invalid\n")...)
			case "overlong-line":
				sample.Transcript = append(sample.Transcript, bytes.Repeat([]byte(" "), 4<<20)...)
			case "oversized-file":
				f, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate((64 << 20) + 1); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if name != "denied" && name != "relative-path" && name != "missing" && name != "directory" && name != "fifo" && name != "oversized-file" {
				if err := os.WriteFile(path, sample.Transcript, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := api.Status(s, id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, got.Context, contextfix.Must(t, "claude-missing-capacity").Expected)
		})
	}
}

func TestContextAPIConsistencyAndRefresh(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"writer", "claude-parent", "claude-missing-capacity", "cursor-2026.10.01-e373342", "cursor-2026.10.12-d47ca18"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sample := contextfix.Must(t, name)
			s, id, path := seedContext(t, sample)
			// The missing-runtime wrapper must retain the same context projection.
			wrapped := s.WithCursorMissingReads(nil)
			for _, view := range []interface {
				api.StatusStore
				api.GetStore
			}{s, wrapped} {
				got, err := api.Status(view, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, got.Context, sample.Expected)
				row, err := api.Get(view, api.RelayView{}, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, row.Context, sample.Expected)
			}
			rec := tmuxfix.NewRecorder()
			client := settleClientFor(t, s.Path(), 0, rec, func() time.Time { return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) })
			status, err := client.Status(id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, status.Context, sample.Expected)
			row, err := client.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, row.Context, sample.Expected)
			if len(rec.Calls()) != 0 || len(rec.SocketCalls()) != 0 {
				t.Error("context read used tmux")
			}
			if sample.Agent == store.AgentCodex {
				gap := contextfix.Variant(t, "compaction-gap")
				if err := os.WriteFile(path, gap.Transcript, 0600); err != nil {
					t.Fatal(err)
				}
				got, err := api.Status(s, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, got.Context, gap.Expected)
				row, err := api.Get(s, api.RelayView{}, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, row.Context, gap.Expected)
				if err := os.WriteFile(path, sample.Transcript, 0600); err != nil {
					t.Fatal(err)
				}
				got, err = api.Status(s, id)
				if err != nil {
					t.Fatal(err)
				}
				assertContext(t, got.Context, sample.Expected)
			}
		})
	}
}

func TestContextDoesNotBorrowArchivedSession(t *testing.T) {
	t.Parallel()
	sample := contextfix.Must(t, "writer")
	archived := codexfix.TempRolloutPath(t, sample.SessionID, true, sample.Transcript)
	oldSession := sample.SessionID
	sample.SessionID = "current-session-without-evidence"
	sample.Transcript = nil
	s, id, _ := seedContext(t, sample, apitest.WithLifeNumber(3), apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: oldSession, JSONLPath: archived, Life: 2}))
	row, err := api.Get(s, api.RelayView{}, id)
	if err != nil {
		t.Fatal(err)
	}
	assertContext(t, row.Context, json.RawMessage(contextfix.UnknownJSON))
	status, err := api.Status(s, id)
	if err != nil {
		t.Fatal(err)
	}
	assertContext(t, status.Context, json.RawMessage(contextfix.UnknownJSON))
}
