package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
)

// A route's own facts (SR-3.4, SR-7.2): its recorded wake endpoint and what
// an owned callback reads before it may drive the coordinator. Nothing here
// delivers, claims or schedules work.

// BusySendWakeFile is the name of a covered Claude Launch's wake endpoint in
// its private launch directory.
const BusySendWakeFile = "busy-send-wake"

// BusySendWakePath is the content-free wake endpoint of the Launch with
// launch token token in store storeID: BusySendWakeFile in that Launch's
// private launch directory, ~/.config/agent-director/launch/<store>/<token>,
// which internal/spawn's launch preparation creates and reclaims. A store or
// token that cannot name one directory is an error.
func BusySendWakePath(storeID, token string) (string, error) {
	for _, component := range []string{storeID, token} {
		if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\\x00") ||
			filepath.Base(component) != component {
			return "", fmt.Errorf("store: invalid launch file identity")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("store: launch files require an absolute home directory")
	}
	return layout.Join(home, "launch", storeID, token, BusySendWakeFile), nil
}

// TouchBusySendWake changes a Launch's recorded wake file's times, the
// content-free notification of its route (SR-7.2): the change is what the
// agent's owned FileChanged hook watches. Only a recorded absolute path is
// touched; none is made up or created. A failed or missing touch is a lost
// wake, which leaves Held Messages for the agent's next eligible event
// (SR-3.1), so it reports nothing.
func TouchBusySendWake(path string) {
	if path == "" || !filepath.IsAbs(path) {
		return
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // a failure is a lost wake, as above
}

// BusySendRouteFacts is what an owned callback of an agent reads, in one
// read transaction, before it may drive delivery (SR-3.3, SR-3.4): whether
// its hook's gate holds, the published current Launch's coverage, route,
// destination and lifecycle, the row's raw state, Turn Start counter and
// permission hold, and the inbox's Held and awaiting counts. It is advisory:
// every store step judges its own captured facts again.
type BusySendRouteFacts struct {
	// GateHolds: the hook's parent is the row's recorded pane process.
	GateHolds bool
	// Covered: the current Launch is covered and has a recorded route.
	Covered bool
	// Ended: the current Launch's End is recorded.
	Ended bool
	// Kind is the current Launch's kind (spawn, reuse, resume, migrated).
	Kind BusySendLaunchKind
	// Destination is the current Launch's, with the row's recorded session.
	Destination BusySendDestination
	// WakePath is the current Launch's recorded wake endpoint, "" for none.
	WakePath string
	// State is the row's raw lifecycle state, never the settle display.
	State string
	// TurnStarts is the row's Turn Start counter for its current Launch.
	TurnStarts int64
	// AgentArgs are the row's recorded agent arguments, whose startup prompt,
	// if any, goes first in a fresh or reused Launch's order (SR-6.1).
	AgentArgs []string
	// PermissionHold: an open permission request holds the row (a Dialog).
	PermissionHold bool
	// Held and Awaiting are the Messages Held now and the current Launch's
	// hand-overs whose input has no evidence it was taken (zero once ended).
	Held, Awaiting int64
}

// ReadBusySendRouteFacts reads instanceID's route facts for a hook with
// gate, in one read transaction, its agent arguments included, so a route
// never judges startup ordering from a separate read. No row is
// ErrSpawnNotFound; any failed read, an undecodable argument list included,
// is an error. It writes nothing.
func (s *Store) ReadBusySendRouteFacts(ctx context.Context, instanceID string, gate HookGate) (BusySendRouteFacts, error) {
	var out BusySendRouteFacts
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		out = BusySendRouteFacts{}
		err := conn.QueryRowContext(ctx, `SELECT CASE WHEN `+hookGateSQL+` THEN 1 ELSE 0 END FROM spawns WHERE instance_id = ?`,
			append(hookGateArgs(gate), instanceID)...).Scan(&out.GateHolds)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSpawnNotFound
		}
		if err != nil {
			return err
		}
		f, err := readBusySendIssueFacts(ctx, conn, instanceID)
		if err != nil {
			return err
		}
		var kind string
		var args sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT l.kind, s.agent_args FROM busy_send_launches l JOIN spawns s ON s.instance_id = l.instance_id
		    WHERE l.launch_id = ?`, f.launch).Scan(&kind, &args); err != nil {
			return err
		}
		if out.AgentArgs, err = decodeArgs(args.String); err != nil {
			return fmt.Errorf("agent_args: %w", err)
		}
		out.Covered = f.launchCoverage == BusySendCovered && f.routeKind != ""
		out.Ended, out.Kind, out.WakePath, out.State = f.launchEnded, BusySendLaunchKind(kind), f.wakePath, f.state
		out.TurnStarts, out.PermissionHold = f.turnStarts, f.permissionHold
		out.Destination = BusySendDestination{LaunchID: f.launch, LaunchToken: f.token, Provider: f.provider, SessionID: f.sessionID,
			RouteKind: BusySendRouteKind(f.routeKind), RouteID: f.routeID, ConnectionGeneration: f.connectionGeneration}
		if out.Held, _, _, err = readBusySendHeld(ctx, conn, f.conversation); err != nil {
			return err
		}
		if !f.launchEnded {
			out.Awaiting, err = readBusySendAwaiting(ctx, conn, f.conversation, f.launch)
		}
		return err
	})
	if errors.Is(err, ErrSpawnNotFound) {
		return BusySendRouteFacts{}, err
	}
	if err != nil {
		return BusySendRouteFacts{}, fmt.Errorf("store: read busy-send route facts: %w", err)
	}
	return out, nil
}
