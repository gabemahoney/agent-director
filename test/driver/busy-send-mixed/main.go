// busy-send-mixed is a generation-specific process driver for M3 T1/T3/T4.
// It contains operations and IPC barriers, never qualification case tables.
package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/version"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

type request struct {
	Op          string          `json:"op"`
	Params      json.RawMessage `json:"params"`
	Before      string          `json:"before,omitempty"`
	AfterBusy   bool            `json:"after_busy,omitempty"`
	AfterCommit bool            `json:"after_commit,omitempty"`
	LoseReply   bool            `json:"lose_reply,omitempty"`
}

type response struct {
	Result        any                       `json:"result"`
	Error         string                    `json:"error,omitempty"`
	UnknownCommit bool                      `json:"unknown_commit,omitempty"`
	StoreBusy     bool                      `json:"store_busy,omitempty"`
	Guard         *store.BusySendGuardError `json:"guard,omitempty"`
	Refusal       *store.BusySendRefusal    `json:"refusal,omitempty"`
}

func mustDecode(raw json.RawMessage, out any) {
	if err := json.Unmarshal(raw, out); err != nil {
		panic(err)
	}
}

func main() {
	if os.Getenv("AGENT_DIRECTOR_TEST_SANDBOX") != "1" {
		panic("mixed-version driver requires the sandbox")
	}
	if len(os.Args) != 3 {
		panic("usage: mixed-driver <store|go> <db-path>")
	}
	mode, path := os.Args[1], os.Args[2]
	in, out := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	if err := out.Encode(map[string]any{"ready": true, "pid": os.Getpid(), "version": version.Version, "commit": version.Commit}); err != nil {
		panic(err)
	}
	var client *api.Client
	if mode == "go" {
		var err error
		client, err = api.New(api.Options{StorePath: path})
		if err != nil {
			panic(err)
		}
		defer client.Close()
	}
	for {
		var req request
		if err := in.Decode(&req); err == io.EOF {
			return
		} else if err != nil {
			panic(err)
		}
		var value any
		var err error
		if mode == "go" {
			value, err = callClient(client, req)
		} else if mode == "store" {
			value, err = callStore(path, req, in, out)
		} else {
			panic("unknown driver mode")
		}
		reply := response{Result: value}
		if err != nil {
			reply.Error, reply.UnknownCommit, reply.StoreBusy = err.Error(), store.CommitUnconfirmed(err), errors.Is(err, store.ErrStoreBusy)
			errors.As(err, &reply.Guard)
			errors.As(err, &reply.Refusal)
		}
		if err := out.Encode(reply); err != nil {
			panic(err)
		}
	}
}

func callClient(c *api.Client, req request) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch req.Op {
	case "send":
		var p api.SendKeysParams
		mustDecode(req.Params, &p)
		return c.SendKeysContext(ctx, p)
	case "lookup":
		var p api.GetDeliveryParams
		mustDecode(req.Params, &p)
		return c.GetDelivery(p)
	case "force-lookup":
		var p api.GetForceParams
		mustDecode(req.Params, &p)
		return c.GetForce(p)
	default:
		return nil, fmt.Errorf("unknown Go operation %q", req.Op)
	}
}

func callStore(path string, req request, in *json.Decoder, out *json.Encoder) (any, error) {
	// Lifecycle testplans need complete published bindings even when their
	// deliberately absent process/socket/token facts prevent a real launch.
	// Fixture catalogue: docs/architecture.md, "Docker lifecycle seed fixture".
	if req.Op == "seed-spawn" {
		var p struct {
			ID, State, Name, Token, Session, JSONL, Parent string
		}
		mustDecode(req.Params, &p)
		id, err := apitest.SeedSpawn(path, p.ID, p.State, "/tmp", "off", p.Session, false,
			apitest.WithLaunchIdentity(store.LaunchIdentity{Token: p.Token}),
			apitest.WithTmuxSessionName(p.Name), apitest.WithJsonlPath(p.JSONL))
		if err == nil && p.Parent != "" {
			err = apitest.SeedParentChild(path, p.Parent, id)
		}
		return id, err
	}
	if req.Op == "seed" {
		var p struct {
			ID, Session, Token, Wake, State string
			Reserved                        int64
			Owned                           bool
		}
		mustDecode(req.Params, &p)
		identity := store.LaunchIdentity{Token: p.Token}
		if p.Owned {
			start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
			if !alive || !known {
				return nil, errors.New("cannot capture fixture pane identity")
			}
			identity.PanePID, identity.PaneStarttime = os.Getpid(), start
		}
		id, err := apitest.SeedSpawn(path, p.ID, p.State, filepath.Dir(path), "off", p.Session, true, apitest.WithLaunchIdentity(identity))
		if err != nil {
			return nil, err
		}
		if err = apitest.SeedBusySendCoverage(path, id, store.BusySendCovered, store.BusySendCovered); err != nil {
			return nil, err
		}
		if err = apitest.SeedBusySendRoute(path, id, "claude_hooks", p.Wake); err != nil {
			return nil, err
		}
		if p.Reserved != 0 {
			err = apitest.SeedBusySendReservation(path, id, p.Reserved, p.Reserved)
		}
		return id, err
	}
	if req.Op == "facts" {
		var id string
		mustDecode(req.Params, &id)
		return apitest.ReadBusySendFacts(path, id)
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if req.Before != "" || req.AfterBusy || req.AfterCommit || req.LoseReply {
		used := false
		barrier := func(name string) error {
			if used {
				return nil
			}
			used = true
			if err := out.Encode(map[string]any{"barrier": name}); err != nil {
				return err
			}
			var release struct {
				Release bool `json:"release"`
			}
			if err := in.Decode(&release); err != nil {
				return err
			}
			if !release.Release {
				return errors.New("barrier requires release")
			}
			return nil
		}
		stop, e := store.ObserveSQLiteForTest(s, func(d driver.Driver) driver.Driver {
			return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
				Before: func(_ context.Context, q string) error {
					if req.Before != "" && strings.Contains(q, req.Before) {
						return barrier("before")
					}
					return nil
				},
				AfterExec: func(_ context.Context, q string, err error) {
					var coded interface{ Code() int }
					if req.AfterBusy && q == "BEGIN IMMEDIATE" && errors.As(err, &coded) && coded.Code()&0xff == 5 {
						// Observe the native failure, then let production retry it
						// within the operation's original context and lock budget.
						if err := barrier("busy"); err != nil {
							panic(err)
						}
					}
				},
				AfterCommit: func(context.Context) error {
					if req.AfterCommit {
						if err := barrier("after_commit"); err != nil {
							return err
						}
					}
					if req.LoseReply {
						return errors.New("mixed-version: lost SQLite COMMIT reply")
					}
					return nil
				},
			})
		})
		if e != nil {
			return nil, e
		}
		defer func() {
			if err := stop(context.Background()); err != nil {
				panic(err)
			}
		}()
	}
	switch req.Op {
	case "identity":
		v, digest := store.BusySendCoreContractIdentity()
		return map[string]string{"core_version": v, "core_declarations_sha256": digest}, nil
	case "admit":
		var p store.BusySendAdmissionRequest
		mustDecode(req.Params, &p)
		return s.WithContext(ctx).AdmitBusySend(p)
	case "lookup":
		var p store.BusySendPair
		mustDecode(req.Params, &p)
		return s.ReadBusySendResult(p.CallerUUID, p.DeliveryRequestID)
	case "destination":
		var id string
		mustDecode(req.Params, &id)
		p, err := s.GetBusySendPublishedLaunch(id)
		if err != nil {
			return nil, err
		}
		r, err := s.GetSpawn(id)
		if err != nil {
			return nil, err
		}
		return store.BusySendDestination{LaunchID: p.LaunchID, LaunchToken: p.LaunchToken, Provider: p.Provider, SessionID: r.SessionID, RouteKind: store.BusySendRouteClaudeHooks}, nil
	case "claim":
		var p store.BusySendClaimRequest
		mustDecode(req.Params, &p)
		return s.ClaimBusySendHandover(ctx, p)
	case "handover":
		var id string
		mustDecode(req.Params, &id)
		return s.ReadBusySendHandover(ctx, id)
	case "row":
		var id string
		mustDecode(req.Params, &id)
		return s.GetSpawn(id)
	case "hook":
		var p struct{ ID, Event, State string }
		mustDecode(req.Params, &p)
		r, err := s.GetSpawn(p.ID)
		if err != nil {
			return nil, err
		}
		gate := store.HookGate{ParentPID: r.Identity.PanePID, ParentStart: r.Identity.PaneStarttime, SessionID: r.SessionID, ClaudeTurnStart: p.Event == "UserPromptSubmit"}
		return s.ApplyHookTransition(p.ID, gate, p.State, false, p.Event, "", false)
	case "end":
		var id string
		mustDecode(req.Params, &id)
		r, err := s.GetSpawn(id)
		if err != nil {
			return nil, err
		}
		return s.EndBusySendLaunch(id, r.Identity.Token)
	case "delete":
		var id string
		mustDecode(req.Params, &id)
		r, err := s.GetSpawn(id)
		if err != nil {
			return nil, err
		}
		scope, err := s.ReadBusySendExitScope(id)
		if err != nil {
			return nil, err
		}
		// Never invent exit evidence for a recorded process still alive.
		for _, launch := range scope.Launches {
			for _, p := range launch.Processes {
				start, alive, known := probe.NewProcChecker().StartTime(p.PID)
				if !known || (alive && start == p.StartTime) {
					return nil, fmt.Errorf("process %d is not proved gone", p.PID)
				}
			}
		}
		return s.DeleteBusySendCovered(id, r.Snapshot, store.BusySendExitEvidence{Scope: scope})
	case "force-claim":
		var p struct {
			ID, ForceID, Owner string
			At                 time.Time
		}
		mustDecode(req.Params, &p)
		launch, err := s.GetBusySendPublishedLaunch(p.ID)
		if err != nil {
			return nil, err
		}
		return s.ClaimBusySendForce(ctx, store.BusySendForceClaimRequest{InstanceID: p.ID, Launch: launch, ForceID: p.ForceID, OwnerCallID: p.Owner, ActiveBudget: time.Hour, CleanupBudget: time.Minute, At: p.At})
	case "force-end":
		var p struct {
			Ref store.BusySendForceRef
			At  time.Time
		}
		mustDecode(req.Params, &p)
		return s.EndBusySendForceCall(ctx, p.Ref, p.At)
	case "issue":
		var p struct {
			Ref   store.BusySendPromptRef
			At    time.Time
			Force *store.BusySendForceRef
		}
		mustDecode(req.Params, &p)
		start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
		if !alive || !known {
			return nil, errors.New("cannot capture driver process identity")
		}
		return s.CommitBusySendIssue(ctx, store.BusySendIssueRequest{Ref: p.Ref, Force: p.Force, Now: func() time.Time { return p.At }, Issuer: &store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: os.Getpid(), StartTime: start}})
	case "release":
		var p store.BusySendReleaseRequest
		mustDecode(req.Params, &p)
		return s.ReleaseBusySendClaims(ctx, p)
	case "complete":
		var p store.BusySendCompletion
		mustDecode(req.Params, &p)
		return s.RecordBusySendCompletion(ctx, p)
	case "evidence":
		var p store.BusySendPromptEvidence
		mustDecode(req.Params, &p)
		return s.RecordBusySendEvidence(ctx, p)
	case "expire":
		var p struct{ Cutoff, At time.Time }
		mustDecode(req.Params, &p)
		return nil, s.ExpireBusySendRetention(p.Cutoff, p.At)
	default:
		return nil, fmt.Errorf("unknown store operation %q", req.Op)
	}
}
