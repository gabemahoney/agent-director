package api_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/google/uuid"
)

func TestResumeDelegationSelection(t *testing.T) {
	// Serial: effective HOME and trail observations.
	e := newDelegateEnv(t)
	old := e.ready(t, "zzz-99", 1, []string{"claude", "codex"}, "", true)
	want := e.ready(t, "aaa-1", 3, []string{"claude", "codex"}, "", true)
	e.ready(t, "newer-uncovered", 4, []string{"claude"}, "", true)
	e.ready(t, "incompatible", 5, []string{"claude", "codex"}, strings.Repeat("f", 64), true)
	e.ready(t, "unfinished", 6, []string{"claude", "codex"}, "", false)
	if err := os.MkdirAll(filepath.Join(e.root, layout.VersionsDir, ".staging-in-progress"), 0700); err != nil {
		t.Fatal(err)
	}
	// Reverse both lexical and timestamp order. Neither denotes newest installed.
	for path, stamp := range map[string]int64{old: 2000000000, want: 1000000000} {
		if err := os.Chtimes(filepath.Dir(path), time.Unix(stamp, 0), time.Unix(stamp, 0)); err != nil {
			t.Fatal(err)
		}
	}
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) { return delegateResult(e.row.ID), true, nil })
	for _, selected := range []string{"", "zzz-99", "absent-version", "aaa-1", "zzz-99"} {
		t.Run("current-"+selected, func(t *testing.T) {
			current := filepath.Join(e.root, layout.CurrentVersion)
			if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if selected != "" {
				if err := os.Symlink(filepath.Join(layout.VersionsDir, layout.VersionID(selected)), current); err != nil {
					t.Fatal(err)
				}
			}
			check := e.untouched(t)
			prior := len(e.calls)
			res, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
			if err != nil || res.InstanceID != e.row.ID || len(e.calls) != prior+1 || e.paths[len(e.paths)-1] != want {
				t.Fatalf("resume = %+v, %v; paths %v; want %s once", res, err, e.paths, want)
			}
			check()
		})
	}
}

func TestResumeDelegationWholeCall(t *testing.T) {
	// Serial: captures HOME and caller environment, reads the shared trail.
	for _, state := range []string{store.StateEnded, store.StatePending, store.StateWorking} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults-%t", state, defaults), func(t *testing.T) {
				e := newDelegateEnv(t)
				// This row cannot be resumed locally: no session, missing cwd and
				// transcript; defaults and trust work must also stay in the recipient.
				id := uuid.NewString()
				_, err := apitest.SeedSpawn(e.dbPath, id, state, filepath.Join(t.TempDir(), "gone"), "off", "", false,
					apitest.WithAgent("codex"), apitest.WithJsonlPath("/missing-transcript"))
				if err != nil {
					t.Fatal(err)
				}
				e.row.ID = id
				seedBusyCoveredConversation(t, e.dbPath, id) // uncovered Launch, covered conversation
				wantPath := e.covering(t)
				p := api.ResumeParams{InstanceID: id}
				if defaults {
					p.SendMode, p.ConfirmedDelivery, p.ConfirmFromTranscripts = ptr("steer_turn"), ptr(false), ptr(false)
				}
				t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "parent-"+uuid.NewString())
				wantParent := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
				e.run(t, func(req resumedelegate.Request) ([]byte, bool, error) {
					wantParams, _ := json.Marshal(p)
					if string(req.Params) != string(wantParams) || req.Home != e.home || req.StorePath != e.dbPath || req.StoreID != e.storeID ||
						req.ConfigPath != filepath.Join(filepath.Dir(e.dbPath), "config.toml") || req.DefaultConfig || req.TmuxCommand != "" ||
						req.Caller.ParentInstanceID != wantParent || req.Caller.PID != os.Getpid() || req.Caller.Process == "" {
						t.Errorf("wrong whole-call references: %+v, params %s; want %s", req, req.Params, wantParams)
					}
					return delegateResult(id), true, nil
				})
				check := e.untouched(t)
				res, err := e.c.Resume(p)
				if err != nil || res.InstanceID != id || len(e.calls) != 1 || e.paths[0] != wantPath {
					t.Fatalf("resume = %+v, %v; calls %d paths %v", res, err, len(e.calls), e.paths)
				}
				check()
			})
		}
	}
}

func TestResumeDelegationGates(t *testing.T) {
	// Serial: scratch HOME; shared trail observations.
	e := newDelegateEnv(t)
	e.covering(t)
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
		t.Fatal("unexpected delegation")
		return nil, false, nil
	})
	check := e.untouched(t)
	_, err := e.c.Resume(api.ResumeParams{InstanceID: "absent", SendMode: ptr("invalid")})
	assertOneName(t, err, "ErrInvalidFlags")
	_, err = e.c.Resume(api.ResumeParams{InstanceID: "absent"})
	assertOneName(t, err, "ErrSpawnNotFound")
	// Reader failure is never interpreted as an uncovered binding/local fallback.
	stop, err := api.ObserveClientSQLiteForTest(e.c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if strings.Contains(q, "LEFT JOIN busy_conversations") {
				return errors.New("binding read failed")
			}
			return nil
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, got := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got == nil || !strings.Contains(got.Error(), "binding read failed") {
		t.Fatalf("binding failure = %v", got)
	}
	check()
	// A supported or uncovered provider keeps the local state gate.
	for _, agent := range []string{"claude", "codex"} {
		id, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, t.TempDir(), "off", "", false, apitest.WithAgent(agent))
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.c.Resume(api.ResumeParams{InstanceID: id})
		assertOneName(t, err, "ErrSpawnNotResumable")
		if !strings.Contains(err.Error(), "state=working") || api.ErrDetails(err) != nil {
			t.Fatalf("local gate = %v, %+v", err, api.ErrDetails(err))
		}
	}
}

func TestResumeDelegationDefaultConfigReferences(t *testing.T) {
	// Serial: the effective HOME is captured at Client construction.
	e := newDelegateEnv(t)
	e.covering(t)
	command := filepath.Join(t.TempDir(), "configured-tmux")
	c, err := api.New(api.Options{StorePath: e.dbPath, TmuxCommand: command, TmuxClient: e.rec})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	api.SetResumeDelegateRunnerForTest(c, func(_ context.Context, _ string, req resumedelegate.Request, _ time.Duration) ([]byte, bool, error) {
		if req.Home != e.home || req.ConfigPath != layout.Join(e.home, layout.ConfigFile) || req.DefaultConfig || req.TmuxCommand != command || req.StorePath != e.dbPath || req.StoreID != e.storeID {
			t.Fatalf("default configuration references: %+v", req)
		}
		return delegateResult(e.row.ID), true, nil
	})
	t.Setenv("HOME", t.TempDir())
	check := e.untouched(t)
	if res, err := c.Resume(api.ResumeParams{InstanceID: e.row.ID}); err != nil || res.InstanceID != e.row.ID {
		t.Fatalf("captured HOME: %+v / %v", res, err)
	}
	check()
}

func TestResumeDelegationDiscoveryFailure(t *testing.T) {
	// Serial: effective HOME and trail observations.
	for _, kind := range []string{"missing", "empty", "root-is-file", "malformed-ready"} {
		t.Run(kind, func(t *testing.T) {
			e := newDelegateEnv(t)
			switch kind {
			case "empty":
				if err := os.MkdirAll(e.root, 0700); err != nil {
					t.Fatal(err)
				}
			case "root-is-file":
				if err := os.MkdirAll(filepath.Dir(e.root), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(e.root, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed-ready":
				path := e.covering(t)
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), layout.VersionRecord), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
				t.Fatal("unexpected delegate")
				return nil, false, nil
			})
			check := e.untouched(t)
			_, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
			if kind == "missing" || kind == "empty" {
				assertOneName(t, err, "ErrSpawnNotResumable")
				want := api.NotResumableDetails{Cause: "covering_version_unavailable", Provider: "codex"}
				if !reflect.DeepEqual(api.ErrDetails(err), want) {
					t.Fatalf("details %+v, want %+v", api.ErrDetails(err), want)
				}
			} else {
				assertOneName(t, err, "ErrInternal")
				if api.ErrDetails(err) != nil || !strings.Contains(err.Error(), "read installed versions") {
					t.Fatalf("diagnostic %v / %+v", err, api.ErrDetails(err))
				}
			}
			check()
		})
	}
}

func TestResumeDelegationOutcomes(t *testing.T) {
	// Serial: effective HOME and trail observations.
	for _, tc := range []struct {
		name, reply string
		started     bool
		runErr      error
		want        string
	}{
		{"success", `{"response_format":1,"result":{"instance_id":"remote","pre_trust":"skipped","future":false,"nullable":null}}`, true, nil, "success"},
		{"valid-despite-exit", `{"response_format":1,"result":{"instance_id":"remote","pre_trust":"skipped"}}`, true, errors.New("exit 1"), "success"},
		{"exec", "", false, os.ErrPermission, "not-started"},
		{"lost", "", true, errors.New("lost pipe"), "unknown"},
		{"truncated", `{"response_format":1,"result":`, true, nil, "unknown"},
		{"malformed", `not JSON`, true, nil, "unknown"},
		{"format", `{"response_format":2,"result":{}}`, true, nil, "unknown"},
		{"terminated", "", true, errors.New("signal: killed"), "unknown"},
		{"deadline", "", true, context.DeadlineExceeded, "unknown"},
		{"bad-result", `{"response_format":1,"result":{"instance_id":false}}`, true, nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDelegateEnv(t)
			e.covering(t)
			e.ready(t, "fallback", 2, []string{"claude", "codex"}, "", true)
			idJSON, err := json.Marshal(e.row.ID)
			if err != nil {
				t.Fatal(err)
			}
			reply := strings.ReplaceAll(tc.reply, `"remote"`, string(idJSON))
			e.run(t, func(resumedelegate.Request) ([]byte, bool, error) { return []byte(reply), tc.started, tc.runErr })
			check := e.untouched(t)
			res, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
			switch tc.want {
			case "success":
				if err != nil || res.InstanceID != e.row.ID || res.PreTrust != "skipped" {
					t.Fatalf("result %+v / %v", res, err)
				}
				var wire resumedelegate.Response
				if err := json.Unmarshal([]byte(reply), &wire); err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(res)
				if string(b) != string(wire.Result) {
					t.Fatalf("result lost fields: %s, want %s", b, wire.Result)
				}
			case "not-started":
				assertOneName(t, err, "ErrInternal")
				if !strings.Contains(err.Error(), "no delegate started") || strings.Contains(err.Error(), "unknown") {
					t.Fatal(err)
				}
			case "unknown":
				assertOneName(t, err, "ErrInternal")
				want := "delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action"
				if err.Error() != want || api.ErrDetails(err) != nil {
					t.Fatalf("unknown result %v / %+v", err, api.ErrDetails(err))
				}
			}
			if len(e.calls) != 1 {
				t.Fatalf("retried %d times", len(e.calls))
			}
			check()
		})
	}
	for _, tc := range []struct {
		name, details string
		sentinel      error
	}{
		{"ErrSpawnNotResumable", `{"cause":"remote-refusal","extra":false,"large":9007199254740993}`, api.ErrSpawnNotResumable},
		{"ErrCantSend", `null`, api.ErrCantSend},
		{"ErrFutureResume", `{"future":null}`, nil},
		{"ErrInternal", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDelegateEnv(t)
			e.covering(t)
			e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
				b, err := json.Marshal(resumedelegate.Response{ResponseFormat: 1, Error: &resumedelegate.Error{Name: tc.name, Description: "unchanged recipient advice", Details: json.RawMessage(tc.details)}})
				if err != nil {
					t.Fatal(err)
				}
				return b, true, errors.New("exit 1")
			})
			check := e.untouched(t)
			_, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
			name, _ := errnames.Classify(err)
			if err == nil || err.Error() != "unchanged recipient advice" || name != tc.name || tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("remote error %v / %s, want %s", err, name, tc.name)
			}
			details := api.ErrDetails(err)
			if tc.details == "" {
				if details != nil {
					t.Fatalf("invented details: %+v", details)
				}
			} else if b, _ := json.Marshal(details); string(b) != tc.details {
				t.Fatalf("details %s, want %s", b, tc.details)
			}
			if len(e.calls) != 1 {
				t.Fatalf("calls %d", len(e.calls))
			}
			check()
		})
	}
}

func TestResumeDelegationOriginalDeadlineAndCaller(t *testing.T) {
	// Serial: caller environment changes after capture are deliberate.
	e := newDelegateEnv(t)
	e.covering(t)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "original-parent")
	deadline := time.Now().Add(time.Second)
	resume := api.CapturedResumeDelegateForTest(e.c, deadline)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "changed-parent")
	e.run(t, func(req resumedelegate.Request) ([]byte, bool, error) {
		if !req.Deadline.Equal(deadline) || req.Caller.ParentInstanceID != "original-parent" {
			t.Fatalf("fresh deadline/caller: %+v", req)
		}
		return nil, true, context.DeadlineExceeded
	})
	check := e.untouched(t)
	_, err := resume(api.ResumeParams{InstanceID: e.row.ID}, "codex")
	assertOneName(t, err, "ErrInternal")
	if len(e.calls) != 1 {
		t.Fatalf("calls %d", len(e.calls))
	}
	// No production timeout is slept out: inject an already-spent allowance.
	expired := api.CapturedResumeDelegateForTest(e.c, time.Now().Add(-time.Second))
	_, err = expired(api.ResumeParams{InstanceID: e.row.ID}, "codex")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no delegate started") || len(e.calls) != 1 {
		t.Fatalf("expired call restarted: %v; calls %d", err, len(e.calls))
	}
	check()
}
