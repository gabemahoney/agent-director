package api

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// This file holds plain spawn's label scan (SR-9.3; WD 2026-09-29 HOOK). It
// lives in pkg/api rather than internal/spawn because the verb layer holds
// the collision pre-check's answer (no row), and the scan's refusal writes
// its ad.launch.name_held record from the verb's pkg/api path (SR-14).

// scanNothingWritten is the scan's consequence sentence: it runs before
// pre-trust and the insert, so a refusal leaves no row and no trust entry.
const scanNothingWritten = "nothing was written and no row was created"

// scanCantTellConsequence is the consequence of the scan's Can't tell
// refusals (SR-1.4): nothing was done, and for the scan that means nothing
// was written (SR-9.3).
const scanCantTellConsequence = nothingWasDone + ": " + scanNothingWritten

// scanLeftoversNamed is how many leftover sessions the refusal names before
// giving the rest as a count (SR-1.4).
const scanLeftoversNamed = 3

// scanForLeftover is plain spawn's label scan (SR-9.3), run for a
// caller-supplied instance id whose collision pre-check found no row of any
// state, after that pre-check and before socket creation, the token,
// pre-trust and the insert; a minted id and a finished row are never scanned.
// It resolves the socket from the caller's environment creating nothing
// (spawn.ResolveScanSocket) and makes one lookup for a token-less launch of
// the id with this store's id and no recorded server identity, so whichever
// server answers counts and only this store's labels are seen.
//
// A session carrying a valid label of this store that names the id, under
// any name and with any token, refuses the spawn with ErrTmuxSessionConflict
// ("left over from an earlier life") and writes one ad.launch.name_held
// record; a Can't tell refuses with its usual error, through the single-row
// verbs' shared cantTellError with the scan's consequence (nothing was
// written); nothing is written in either case. Gone (no such label, only
// other stores', foreign or invalid labels, no server, no socket) proceeds:
// the requested name's holder is not judged here. It is the one place the
// scan's Leftover maps to a spawn error (SR-1.8).
func scanForLeftover(t tmux.LookupClient, pc ProcChecker, storeID, instanceID string) error {
	socket, err := spawn.ResolveScanSocket()
	if err != nil {
		return err
	}
	res := tmux.Lookup(t, pc, tmux.Launch{InstanceID: instanceID, StoreID: storeID, Socket: socket}, "")
	switch res.Verdict {
	case tmux.Gone:
		return nil
	case tmux.Leftover:
		leftovers := sortedBySessionNumber(res.Leftovers)
		emitScanNameHeld(instanceID, socket, storeID, res.Token(), leftovers)
		return scanLeftoverError(instanceID, leftovers)
	case tmux.CantTell:
		return cantTellError(res, cantTellRefusal{
			InstanceID:  instanceID,
			Context:     "the label scan before the spawn",
			Socket:      socket,
			Call:        tmux.CallLookup,
			Consequence: scanCantTellConsequence,
		})
	}
	// Ours cannot arise for a launch with no token (SR-3.4).
	return fmt.Errorf("spawn: label scan: unexpected lookup outcome %q; %s", res.Token(), scanNothingWritten)
}

// scanLeftoverError is the scan's leftover refusal (SR-1.4): the instance id;
// "left over from an earlier life"; the quoted name and tmux id of each
// session found, up to three, then the rest as a count; that nothing was
// written; that ending a session is a human's decision, with the pointer to
// the README's "Operator actions"; and "list --tmux-session-name". It never
// carries a label value, a token or a store id.
func scanLeftoverError(instanceID string, leftovers []tmux.Session) error {
	named := make([]string, 0, scanLeftoversNamed)
	for _, s := range leftovers[:min(len(leftovers), scanLeftoversNamed)] {
		named = append(named, fmt.Sprintf("%q (%s)", s.Name, s.ID))
	}
	found := strings.Join(named, ", ")
	if more := len(leftovers) - len(named); more > 0 {
		found += fmt.Sprintf(" and %d more", more)
	}
	return fmt.Errorf("%w: instance %s: left over from an earlier life: %d tmux session(s) labelled by this agent-director store with this instance id still run: %s; %s; "+
		"ending such a session is a human's decision, %s; %s",
		tmux.ErrTmuxSessionConflict, instanceID, len(leftovers), found, scanNothingWritten, operatorActionsPointer, listSessionNameHint)
}

// emitScanNameHeld writes the scan refusal's one ad.launch.name_held record
// (SR-9.3, SR-14), fail-open: a trail-write failure never changes the
// result. The first leftover (lowest numeric $N) gives the session fields and
// the by-hand commands, which humans read; no error description carries
// them. It writes no ad.provenance.disagree: a leftover is expected.
func emitScanNameHeld(instanceID, socket, storeID, lookupOutcome string, leftovers []tmux.Session) {
	first := leftovers[0]
	who := callerIdentity()
	_ = trail.Emit(context.Background(), "ad.launch.name_held", map[string]any{
		"source":             "ad_spawn",
		"claude_instance_id": instanceID,
		"launch":             "spawn",
		"tmux_session_name":  first.Name,
		"tmux_socket":        socket,
		"tmux_session_id":    first.ID,
		"session_created":    first.Created,
		"store_id":           storeID,
		"carries_this_id":    true,
		"current_launch":     false,
		"lookup_outcome":     lookupOutcome,
		"outcome":            "ErrTmuxSessionConflict",
		"row_result":         "not_inserted",
		"leftover_count":     len(leftovers),
		"store_error":        nil,
		"attach_command":     "tmux -u -S " + shellQuote(socket) + " attach-session -r -t " + shellQuote(first.ID),
		"end_command":        "tmux -u -S " + shellQuote(socket) + " kill-session -t " + shellQuote(first.ID),
		"caller_process":     who.process,
		"caller_pid":         who.pid,
		"caller_hostname":    who.hostname,
		"caller_user":        who.user,
	})
}

// sortedBySessionNumber returns a copy of sessions ordered by the number of
// their tmux id ($N), lowest first; an id that is not $ and digits sorts
// last. The first is the "first leftover found" of SR-9.3.
func sortedBySessionNumber(sessions []tmux.Session) []tmux.Session {
	out := slices.Clone(sessions)
	slices.SortStableFunc(out, func(a, b tmux.Session) int {
		return cmp.Compare(sessionNumber(a.ID), sessionNumber(b.ID))
	})
	return out
}

// sessionNumber is the N of a tmux session id "$N", or math.MaxInt when id
// has another form.
func sessionNumber(id string) int {
	digits, ok := strings.CutPrefix(id, "$")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || n < 0 {
		return math.MaxInt
	}
	return n
}

// shellQuote quotes s for a POSIX shell in single quotes, each ' written as
// '\”, so a by-hand command is copied exactly (SR-14).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
