package api

import (
	"context"
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// UnownedSession is one tmux session find-missing found labelled with this
// store's id whose instance id matches no row of any status (b.9ps): an agent
// session no row owns, so no other verb shows it. It is reported only.
type UnownedSession struct {
	// InstanceID is the instance id the session's label names.
	InstanceID string `json:"instance_id"`
	// TmuxSocket is the socket of the tmux server holding the session.
	TmuxSocket string `json:"tmux_socket"`
	// TmuxSessionID is the session's tmux id, "$N".
	TmuxSessionID string `json:"tmux_session_id"`
	// TmuxSessionName is the session's name as tmux lists it.
	TmuxSessionName string `json:"tmux_session_name"`
	// CreatedAt is the session's creation time (#{session_created}), RFC 3339
	// in UTC.
	CreatedAt string `json:"created_at"`
}

// unownedSessions is find-missing's unowned_sessions report (b.9ps), taken
// once the run's rows are judged so they keep first claim on the budget.
//
// It reads every distinct non-empty tmux_socket of rows (the run's selection,
// finished rows included) and the caller's default socket (sweepSockets.forRow
// for a row that records none), each through live's Sweep (tmux.Sweep.Answer):
// a socket a row already looked up gives its held answer, and a stopped socket,
// a spent budget or a failed call gives nothing. A server with an @ad_owner
// scope value gives nothing either, since its labels cannot be trusted. A
// session counts when its label is valid, carries this store's id and names
// an instance id no row of rows has. A non-nil recheck certifies that rows came
// from the all-status RecoveryRows selection, and re-reads candidate ids so a
// row written during the run owns its session; a failed read reports nothing.
// Without that capability, live-only membership cannot establish that a session
// is unowned: the report is empty and makes no lookup or socket resolution.
//
// It makes no write and no trail record. The result is sorted by socket,
// session id and instance id, and is [] when live is nil, the run has no tmux
// client or ctx has ended.
func unownedSessions(ctx context.Context, live *findMissingRun, rows []Spawn, recheck func([]string) ([]Spawn, error)) []UnownedSession {
	out := []UnownedSession{}
	if recheck == nil || live == nil || live.t == nil || ctx.Err() != nil {
		return out
	}
	owned := map[string]bool{}
	seen := map[string]bool{}
	var sockets []string
	for _, row := range rows {
		owned[row.InstanceID] = true
		if s := row.Identity.Socket; s != "" && !seen[s] {
			seen[s] = true
			sockets = append(sockets, s)
		}
	}
	sort.Strings(sockets)
	if def, _, ok := live.sockets.forRow(""); ok && !seen[def] {
		sockets = append(sockets, def)
	}
	storeID := live.s.StoreID()
	for _, socket := range sockets {
		if ctx.Err() != nil {
			return []UnownedSession{}
		}
		ans, ok := live.sw.Answer(socket)
		if !ok || ans.ScopeValue {
			continue
		}
		for _, sess := range ans.Sessions {
			lb := sess.Label
			if lb.Kind != tmux.LabelValid || storeID == "" || lb.StoreID != storeID || owned[lb.InstanceID] {
				continue
			}
			out = append(out, UnownedSession{InstanceID: lb.InstanceID, TmuxSocket: socket, TmuxSessionID: sess.ID,
				TmuxSessionName: sess.Name, CreatedAt: time.Unix(sess.Created, 0).UTC().Format(time.RFC3339)})
		}
	}
	if ctx.Err() != nil {
		return []UnownedSession{}
	}
	if len(out) > 0 {
		ids := make([]string, 0, len(out))
		seenIDs := map[string]bool{}
		for _, u := range out {
			if !seenIDs[u.InstanceID] {
				ids = append(ids, u.InstanceID)
				seenIDs[u.InstanceID] = true
			}
		}
		sort.Strings(ids)
		current, err := recheck(ids)
		if err != nil {
			logFindMissing(live.lg, "RecoveryRows(unowned_sessions)", "", err)
			return []UnownedSession{}
		}
		for _, row := range current {
			owned[row.InstanceID] = true
		}
		kept := out[:0]
		for _, u := range out {
			if !owned[u.InstanceID] {
				kept = append(kept, u)
			}
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TmuxSocket != b.TmuxSocket {
			return a.TmuxSocket < b.TmuxSocket
		}
		if a.TmuxSessionID != b.TmuxSessionID {
			return a.TmuxSessionID < b.TmuxSessionID
		}
		return a.InstanceID < b.InstanceID
	})
	return out
}
