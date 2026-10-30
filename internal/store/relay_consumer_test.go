package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Adapt the shared clock and process fixtures to Handle's two narrow interfaces.
// This external store test can use its test-only SQLite COMMIT observer while
// invoking the real public hook consumer, without a production test seam.
type commitHookClock struct {
	*tmuxfix.Clock
	first func()
}

func (c *commitHookClock) Sleep(_ context.Context, d time.Duration) {
	c.Advance(d)
	if fn := c.first; fn != nil {
		c.first = nil
		fn()
	}
}

type commitHookParent struct{ *procfix.Checker }

func (p commitHookParent) CommandName(int) (string, bool) { return "claude", true }
func (p commitHookParent) PPID(int) (int, bool)           { return 0, false }

func TestHookCommittedClaimOutputBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                string
		timeout, late, lost bool
	}{
		{name: "ack positive"}, {name: "ack late", late: true}, {name: "ack lost result", lost: true},
		{name: "own timeout positive", timeout: true}, {name: "own timeout late", timeout: true, late: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "id-d-1"
			s, path := apitest.SeedDecideFixtureAgent(t, "on", store.AgentClaude)
			clock := &commitHookClock{Clock: tmuxfix.NewClock(time.Now())}
			var commits atomic.Int32
			var armed atomic.Bool
			var token string
			decision, origin := "allow", store.DecisionOriginCaller
			if tc.timeout {
				decision, origin = "deny", store.DecisionOriginTimeout
			}
			only := func(reader *store.Store) (store.PermissionRow, error) {
				rows, err := reader.PermissionRequestsForSpawn(id)
				if err != nil {
					return store.PermissionRow{}, err
				}
				if len(rows) != 1 {
					return store.PermissionRow{}, fmt.Errorf("request count=%d", len(rows))
				}
				return rows[0], nil
			}
			clock.first = func() {
				pr, err := only(s)
				if err != nil {
					t.Fatal(err)
				}
				token = pr.RequestToken
				if !tc.timeout {
					if ok, err := s.DecideRelayRequest(id, token, "allow", "", store.WriterProcessDecide, time.Time{}, time.Second); err != nil || !ok {
						t.Fatalf("decide=%v %v", ok, err)
					}
				}
				armed.Store(true)
				store.ObserveDecisionBoundaryForTest(t, s, path, nil, func(context.Context) error {
					if !armed.Load() {
						return nil
					}
					// The callback runs only after the real driver's successful COMMIT.
					reader, err := store.Open(path)
					if err != nil {
						return err
					}
					defer reader.Close()
					pr, err := only(reader)
					if err != nil {
						return err
					}
					if pr.Decision != decision || pr.DecisionOrigin != origin || pr.ResponseState != store.ResponseRelayClaimed || pr.DeliveredAt.IsZero() || pr.ProvenGone() {
						return fmt.Errorf("COMMIT did not preserve expected claim: %+v", pr)
					}
					commits.Add(1)
					if tc.late {
						clock.Advance(10 * time.Second)
					}
					if tc.lost {
						return errors.New("injected lost successful COMMIT result")
					}
					return nil
				})
			}
			var stdout bytes.Buffer
			err := storefix.WithSeedPane(path, id, func(gate store.HookGate) error {
				parent := commitHookParent{procfix.New()}
				parent.Set(gate.ParentPID, procfix.Alive(gate.ParentStart))
				hc := hook.HandleConfig{
					Env: func(key string) string {
						switch key {
						case "AGENT_DIRECTOR_INSTANCE_ID":
							return id
						case hook.EnvRelayMode:
							return "on"
						}
						return ""
					},
					Clock: clock, Now: clock.Now, RelayTimeout: 10 * time.Second,
					ParentPID: func() int { return gate.ParentPID }, ParentProc: parent,
					Self: func() store.ProcessIdentity {
						return store.ProcessIdentity{PID: 54321, Starttime: "synthetic-hook", PIDNamespace: "pid:[4026531836]"}
					},
				}
				payload := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"commit-hook"}`
				return hook.Handle(context.Background(), strings.NewReader(payload), &stdout, s, hc, log.New(io.Discard, "", 0))
			})
			if err != nil {
				t.Fatal(err)
			}
			armed.Store(false)
			if commits.Load() != 1 {
				t.Fatalf("successful claim COMMIT observations=%d; output=%q", commits.Load(), stdout.String())
			}
			want := ""
			if !tc.late && !tc.lost {
				want = hook.EncodeDecision(hook.EventNamePermissionRequest, decision, "") + "\n"
			}
			if stdout.String() != want {
				t.Fatalf("stdout=%q want=%q", stdout.String(), want)
			}
			reader, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			before, err := only(reader)
			if err != nil {
				t.Fatal(err)
			}
			if before.DecisionOrigin != origin || before.ResponseState != store.ResponseRelayClaimed || before.DeliveredAt.IsZero() || before.ProvenGone() {
				t.Fatalf("reopened claim=%+v", before)
			}
			if _, _, acked, err := reader.AckRelayDecision(id, token, clock.Now(), time.Second, nil); err != nil || acked {
				t.Fatalf("reopened claim replay=%v %v", acked, err)
			}
			after, err := only(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || stdout.String() != want {
				t.Fatal("recovery changed or replayed committed claim")
			}
		})
	}
}
