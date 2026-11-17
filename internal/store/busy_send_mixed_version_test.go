package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/version"
)

// TestMixedVersionStoreDriver is only a subprocess entry, not a second test
// matrix. The owning test/mixed-version package supplies cases and expectations.
// Reuse the existing full-record seed instead of copying its SQL to a program.
func TestMixedVersionStoreDriver(t *testing.T) {
	path := os.Getenv("AD_MIXED_STORE_PATH")
	if path == "" {
		t.Skip("generation-specific store helper")
	}
	if os.Getenv("AGENT_DIRECTOR_TEST_SANDBOX") != "1" {
		t.Fatal("sandbox required")
	}
	in, out := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := out.Encode(map[string]any{"ready": true, "version": version.Version, "commit": version.Commit}); err != nil {
		t.Fatal(err)
	}
	identifier := regexp.MustCompile(`^[a-z_][a-z_0-9]*$`)
	for {
		var req struct {
			Op     string `json:"op"`
			Params struct {
				Table, Reader, Key, ID string
				Set                    map[string]any
			}
		}
		if err := in.Decode(&req); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		var value any
		var err error
		p := req.Params
		switch req.Op {
		case "seed-records":
			if err = s.InsertPending(Spawn{InstanceID: "rec", Agent: AgentClaude, CWD: "/tmp", TmuxSessionName: "ad-mixed-records", RelayMode: "off"}); err != nil {
				break
			}
			mustExec(t, s.db, `DELETE FROM busy_send_launches WHERE instance_id='rec'`)
			mustExec(t, s.db, `DELETE FROM busy_conversations WHERE instance_id='rec'`)
			r := seedBusySendRecords(t, s.db, s.StoreID(), "rec")
			mustExec(t, s.db, `UPDATE spawns SET busy_conversation_id=?,busy_send='covered',life_number=?,launch_token=? WHERE instance_id='rec'`, r.conversation, recLife, recToken)
			value = map[string]string{"caller": r.callerUUID, "token": recToken}
		case "record-read":
			value, err = mixedVersionRecord(s, p.Reader, p.ID)
		case "fixture-set":
			for _, id := range []string{p.Table, p.Key} {
				if !identifier.MatchString(id) {
					t.Fatalf("unsafe fixture identifier %q", id)
				}
			}
			var columns []string
			for k := range p.Set {
				if !identifier.MatchString(k) {
					t.Fatal(k)
				}
				columns = append(columns, k)
			}
			sort.Strings(columns)
			var fields []string
			var args []any
			for _, k := range columns {
				fields = append(fields, k+"=?")
				args = append(args, p.Set[k])
			}
			args = append(args, p.ID)
			r, e := s.db.Exec("UPDATE "+p.Table+" SET "+strings.Join(fields, ",")+" WHERE "+p.Key+"=?", args...)
			err = e
			if err == nil {
				value, err = r.RowsAffected()
			}
		case "fixture-force-id":
			// Public force lookup validates UUID syntax; the migration seed's
			// descriptive private key is renamed only as fixture setup.
			tx, e := s.db.Begin()
			if e != nil {
				err = e
				break
			}
			for _, q := range []string{"PRAGMA defer_foreign_keys=ON", "UPDATE busy_send_forces SET force_id=?", "UPDATE busy_send_force_members SET force_id=?", "UPDATE busy_send_handovers SET force_id=? WHERE force_id IS NOT NULL", "UPDATE busy_conversations SET handover_force_id=? WHERE handover_force_id IS NOT NULL"} {
				var args []any
				if strings.Contains(q, "?") {
					args = []any{p.ID}
				}
				if _, err = tx.Exec(q, args...); err != nil {
					break
				}
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			value = p.ID
		default:
			t.Fatalf("unknown helper role %q", req.Op)
		}
		reply := map[string]any{"result": value}
		if err != nil {
			reply["error"] = err.Error()
		}
		if err := out.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}

// Expose decoded production records, not arbitrary SQL columns. Related enum
// fields travel together; expectations and the only case catalogue live in
// test/mixed-version. Decision-only fields use their production consumer.
func mixedVersionRecord(s *Store, reader, id string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch reader {
	case "spawn":
		r, err := s.GetSpawn(id)
		return map[string]any{"busy_send": r.BusySend}, err
	case "route":
		r, err := s.ReadBusySendRouteFacts(ctx, "rec", HookGate{})
		return map[string]any{"kind": r.Kind, "route_kind": r.Destination.RouteKind}, err
	case "exit-scope":
		r, err := s.ReadBusySendExitScope("rec")
		ids := []string{}
		for _, l := range r.Launches {
			ids = append(ids, l.LaunchID)
		}
		return ids, err
	case "deleted-force":
		f, deleted, err := s.RefuseBusySendForceForDeleted(ctx, BusySendForceDeletedRequest{
			ForceID: "deleted-force", OwnerCallID: "reader-call",
			Pair: BusySendPair{CallerUUID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", DeliveryRequestID: "req-1"}, At: time.UnixMilli(6000),
		})
		return map[string]any{"deleted": deleted, "outcome": f.Outcome, "reason": f.Reason, "conversation": f.ConversationID}, err
	case "publish":
		if _, err := s.PublishBusySendLaunch("rec", 0, recToken); err != nil {
			return nil, err
		}
		return mixedVersionRecord(s, "published", "rec")
	case "message":
		r, err := s.ReadBusySendResult("aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "req-2")
		out := map[string]any{"lookup": r.Lookup}
		if r.Result != nil {
			out["send_mode"] = r.Result.SendMode
		}
		return out, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	switch reader {
	case "published":
		r, err := readBusySendPublished(ctx, conn, "rec")
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{"found": false}, nil
		}
		return map[string]any{"found": true, "conversation": r.conversation, "launch": r.launch,
			"coverage": r.coverage, "launch_coverage": r.launchCoverage, "send_mode": r.defaults.SendMode, "route_kind": r.routeKind}, err
	case "processes":
		r, err := readBusySendLaunchFacts(ctx, conn, id)
		return r.processes, err
	case "custody":
		members, err := readBusySendEligiblePrefix(ctx, conn, "rec-conv-2", BusySendBoundaryTurnEnd)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, m := range members {
			ids = append(ids, m.messageID)
		}
		out := map[string]any{"held": ids}
		for _, place := range []BusySendPlace{BusySendClaimed, BusySendIssued} {
			ok, err := busySendPromptMembersIn(ctx, conn, "rec-handover-2", 0, place)
			if err != nil {
				return nil, err
			}
			out[string(place)] = ok
		}
		return out, nil
	case "handover":
		r, err := readBusySendHandover(ctx, conn, s.StoreID(), id)
		return map[string]any{"route_kind": r.Destination.RouteKind, "boundary": r.Boundary, "open": r.Open}, err
	case "prompt":
		r, err := readBusySendPrompt(ctx, conn, id, 0)
		return map[string]any{"emission": r.emission, "transport": r.transport, "order_kind": r.orderKind,
			"admission_kind": r.admission, "evidence_kind": r.evidence, "negative_kind": r.negative}, err
	case "force":
		r, found, err := readBusySendForceRow(ctx, conn, s.StoreID(), id)
		return map[string]any{"found": found, "retention": r.Retention}, err
	case "pause":
		r, found, err := readBusySendPauseClaim(ctx, conn, s.StoreID(), id)
		return map[string]any{"found": found, "signal_kind": r.SignalKind, "state": r.State,
			"call_outcome": r.CallOutcome, "call_cause": r.CallCause, "closed_by": r.ClosedBy}, err
	default:
		return nil, fmt.Errorf("unknown production reader %q", reader)
	}
}
