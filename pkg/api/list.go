package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// ErrListInvalidLabel is returned by the list verb when a caller-supplied
// label filter cannot be parsed as `key=value`. The error name is stable so
// the CLI envelope can pin it; the description carries the offending raw input.
var ErrListInvalidLabel = errors.New("ErrListInvalidLabel")

// ListStore is the narrow store surface List needs. *store.Store
// satisfies it via ListSpawns and OpenPermissionRequestsForSpawn; tests fake
// the surface so the verb's filter-translation can be exercised without
// driving SQLite. A ListStore that also records hook_gone_at (*store.Store
// does) gets that write from List; one that does not reports the stored
// value only.
type ListStore interface {
	ListSpawns(f ListFilters) ([]Spawn, error)
	// OpenPermissionRequestsForSpawn reads a Spawn's permission requests that
	// still await an answer (b.146 rule 9).
	OpenPermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
}

// ListParams is the typed parameter shape for the list verb.
// State accepts a comma-separated slice from the CLI parser ("a,b"
// becomes []string{"a","b"}). Labels arrives as a flat slice of
// "k=v" strings — List parses it into the store's typed filter map
// so the JSON / CLI / MCP surfaces share one validation seam.
type ListParams struct {
	// State filters by lifecycle state or Displayed State: a row matches when
	// its state or its displayed_state is one of the values. Multiple values
	// OR together. Nil/empty means no state filter (all states returned).
	State []string
	// Labels filters by label key=value pairs. Each entry must be "k=v"
	// (returns [ErrListInvalidLabel] otherwise). Multiple entries AND together.
	Labels []string
	// Parent filters by parent_id exact match. Empty means no filter.
	Parent string
	// Cwd filters by canonicalized cwd exact match. Empty means no filter.
	Cwd string
	// TmuxSessionName filters by tmux session name exact match. Empty means
	// no filter. Matches both live and ended rows.
	TmuxSessionName string
	// Limit caps the number of rows returned. 0 means no cap. A negative
	// value is refused with [ErrInvalidFlags].
	Limit int
}

// ListRow mirrors a returned Spawn at the JSON wire level. The store
// returns a richer struct; the verb projects only the fields the SRD
// §12 list shape promises so future store-internal additions don't
// leak silently.
type ListRow struct {
	// InstanceID is the stable id of the Spawn.
	InstanceID string `json:"instance_id"`
	// ParentID is the id of the spawning Spawn. Omitted from JSON when empty.
	ParentID string `json:"parent_id,omitempty"`
	// State is the current lifecycle state. One of: pending, waiting, working,
	// ask_user, check_permission, ended, missing; never a Displayed State,
	// which DisplayedState carries beside it. pending means a launch (spawn,
	// reuse or resume) is in progress and the agent has not reported in yet
	// (its SessionStart hook); it may be loading or waiting at a startup
	// prompt. A resumed pending row keeps its session id and history.
	State string `json:"state"`
	// DisplayedState is, as StatusResult's, the Displayed State the row shows,
	// present only while one shows; its detail only get and status carry.
	DisplayedState string `json:"displayed_state,omitempty"`
	// FamilyState reports working or waiting for the complete stored family.
	// Both directions settle independently using [state] settle_ms.
	FamilyState string `json:"family_state"`
	// ProcessLiveness is alive, exited or unknown: the recorded agent
	// process at its recorded start identity, never its Helper, tmux session
	// or descendants (Cursor: unknown, unsupported). Present on every row.
	ProcessLiveness string `json:"process_liveness"`
	// Confirmations is exactly the state, process_liveness and
	// displayed_state records (Confirmation). family_state has none.
	Confirmations Confirmations `json:"confirmations"`
	// IdleScreenAvailability is, as StatusResult's, available for Claude and
	// Codex or an absent agent and unavailable for Cursor. Present on every row.
	IdleScreenAvailability string `json:"idle_screen_availability,omitempty"`
	// CWD is the canonicalized working directory the Spawn was started in.
	CWD string `json:"cwd"`
	// Agent is the agent the row runs, "claude", "codex" or "cursor": set by the spawn
	// that most recently launched the row, followed by
	// resume, never changed by anything else (SR-1.5).
	Agent string `json:"agent"`
	// TmuxSessionName is the tmux session name for the Spawn.
	TmuxSessionName string `json:"tmux_session_name"`
	// RelayMode is "on" or "off".
	RelayMode string `json:"relay_mode"`
	// Labels are the caller-supplied key-value tags set at spawn time.
	Labels map[string]string `json:"labels"`
	// StartedAt is the row-insert time.
	StartedAt time.Time `json:"started_at"`
	// LastSeenAt is the most recent hook UPSERT time.
	LastSeenAt time.Time `json:"last_seen_at"`
	// StateChangedAt is, as SpawnRow.StateChangedAt, when the raw state last
	// changed, RFC3339 UTC with millisecond precision (b.k6i); the Unix epoch
	// when it has not changed since the stamp was added. Present on every row.
	// A Cursor row the list finds lost shows missing at once; this moves when
	// that mark is stored, which can be after the read that first shows missing.
	StateChangedAt time.Time `json:"state_changed_at"`
	// EndedAt is set when state moves to ended. Omitted from JSON while live.
	EndedAt *time.Time `json:"ended_at,omitempty"`
	// LaunchStartedAt is the start of the launch in progress, RFC3339 UTC
	// with millisecond precision. Present only on a pending row; nil (and
	// omitted from JSON) otherwise (SR-22.2).
	LaunchStartedAt *time.Time `json:"launch_started_at,omitempty"`
	// LivenessUnverifiedSince is, as SpawnRow.LivenessUnverifiedSince, the
	// RFC3339 timestamp of the first check that noted this live row (left it
	// unverified, or noted it unreported, b.kdf), kept while later checks
	// change LivenessNote, unreported included. Nil (omitted from JSON) when
	// NULL in the store: a check that finds the agent process alive clears it
	// together with LivenessNote, except on a pending row it notes unreported
	// and a pending row noted provenance_conflict, which keeps it. The store
	// carries it as a COALESCE-scanned string ("" == NULL); List maps "" to
	// nil per the ended_at nullable precedent.
	LivenessUnverifiedSince *string `json:"liveness_unverified_since,omitempty"`
	// LivenessNote is, as SpawnRow.LivenessNote, either the reason token of
	// the latest check that left this live row unverified (overwritten when
	// the reason changes, cleared when a check finds the agent process alive,
	// except on a pending row it notes unreported and a pending row noted
	// provenance_conflict, which keeps it) or unreported, on a pending row
	// whose agent is alive but has not reported through any hook since its
	// launch (b.kdf): the row stays pending, the caller reads its pane and,
	// only having looked, sends keys with allow_pending, a later sweep that
	// finds the agent alive keeps the note, and the agent's next hook clears
	// it; unreported never replaces provenance_conflict. Nil (omitted) when
	// NULL in the store. Same "" == NULL mapping.
	LivenessNote *string `json:"liveness_note,omitempty"`
	// PermissionRequests is, as SpawnRow.PermissionRequests, the row's open
	// permission requests (those that still await an answer), each with its
	// delivery facts (b.146 rule 15). Populated only when the row's live
	// (raw) state is check_permission, as SpawnRow.PermissionRequests; always
	// a non-nil slice (encodes as [] when empty).
	PermissionRequests []PermissionRequestInfo `json:"permission_requests"`
}

// ListResult is the typed return shape. Spawns is always a non-nil
// slice (possibly empty) so the JSON envelope encodes as `[]`, never
// `null`.
type ListResult struct {
	// Spawns is the slice of matching rows. Never nil — encodes as [] when empty.
	Spawns []ListRow `json:"spawns"`
}

// List enumerates Spawn rows matching the supplied filter set.
// Behavior (SRD §12 + §4.2):
//
//   - All filters AND together. An absent filter is permissive.
//   - State accepts multiple values, OR'd together server-side.
//   - Labels match by exact JSON value via json_extract.
//   - Parent and Cwd are exact-match against the column.
//   - Limit caps the result count; 0 means "no cap".
//   - Returned order is unspecified. Callers wanting stable order
//     sort the slice themselves (e.g. via jq).
//
// Validation: a negative Limit yields ErrInvalidFlags, refused rather than
// read as "no cap" (b.c4n). Every Labels entry must contain a literal `=`.
// A label of the form `foo` (no separator) yields ErrListInvalidLabel. Empty
// key (`=v`) is also rejected so the json_extract path never receives an
// empty key string. On either refusal the store is never reached.
//
// Every failure (a negative limit, an invalid label, the store read's error,
// the permission-request read's error, or ErrClientClosed from (c
// *Client).List) returns Spawns as a non-nil empty slice, as ListResult
// documents (b.hbt).
//
// Each row in check_permission carries its open permission requests with
// their delivery facts, judged through v and read as Get reads them (the
// check-before-read rule, b.146 rule 5). List is a reading verb and never
// waits for the store's write lock (b.146 problem 4): it writes hook_gone_at
// on every request it found fallen back, in one write, only if the lock is
// free at that moment, and attempts no such write once a display observation
// write of the same read has found the store busy.
//
// List reads with no settle time and samples no pane, so a working Claude or
// Codex row keeps its pane's current idle_screen finding as committed; the
// Client applies its [state] settle_ms and samples panes (listSettled).
func List(s ListStore, v RelayView, params ListParams) (ListResult, error) {
	return listSettled(s, v, store.SettleRead{}, params)
}

// listSettled is List with the reader's settle time and clock settle (b.3ie),
// passed to the store as ListFilters.Settle: each row's state field is its
// settled state. The filters and LIMIT apply once, in the store's statement,
// on stored facts: a row matches a State filter by its state or its committed
// displayed_state (ListFilters.StoredDisplay). The checks then run on those
// fixed IDs only, and every selected row is returned even when a check changes
// it; no filter is reapplied and no freed limit slot is filled.
// launch_started_at and whether a row's permission requests are read and
// shown key on the raw state, as before (permission requests are never
// debounced). With no settle time it is List exactly as before b.3ie. It
// never waits for a state to settle.
func listSettled(s ListStore, v RelayView, settle store.SettleRead, params ListParams) (ListResult, error) {
	filters, err := parseListFilters(params, settle)
	if err != nil {
		return ListResult{Spawns: []ListRow{}}, err
	}
	return listFiltered(s, v, filters)
}

// parseListFilters validates before either candidate enumeration or projection.
func parseListFilters(params ListParams, settle store.SettleRead) (ListFilters, error) {
	if params.Limit < 0 {
		return ListFilters{}, fmt.Errorf("%w: limit = %d is negative; pass 0 (or omit it) for no cap, or a positive number of rows",
			ErrInvalidFlags, params.Limit)
	}
	labels := make(map[string]string, len(params.Labels))
	for _, raw := range params.Labels {
		idx := strings.IndexByte(raw, '=')
		if idx <= 0 {
			return ListFilters{}, fmt.Errorf("%w: %q is not in key=value form", ErrListInvalidLabel, raw)
		}
		labels[raw[:idx]] = raw[idx+1:]
	}

	return ListFilters{
		State:           params.State,
		Labels:          labels,
		Parent:          params.Parent,
		Cwd:             params.Cwd,
		TmuxSessionName: params.TmuxSessionName,
		Limit:           params.Limit,
		Settle:          settle,
	}, nil
}

// listFiltered is listSettled past validation. Stored filters and limit fix
// the returned IDs before any row is checked.
func listFiltered(s ListStore, v RelayView, filters ListFilters) (ListResult, error) {
	if concrete, ok := s.(*store.Store); ok {
		s = concrete.WithRecoveryTickSource("ad_recovery")
	}
	if _, checked := s.(*activeReadStore); !checked {
		selected := filters
		selected.StoredDisplay = true
		rows, err := s.ListSpawns(selected)
		if err != nil {
			return ListResult{Spawns: []ListRow{}}, err
		}
		run := checkActiveRows(context.Background(), s, v, filters.Settle, rows, activeRefresh(s, filters.Settle, false), false)
		out, err := listFiltered(run, run.view, ListFilters{Settle: filters.Settle})
		var fallbacks map[string]cursorListFallback
		if err == nil {
			fallbacks = cursorListFallbacks(run)
		}
		finishErr := run.finish()
		if err != nil {
			return ListResult{Spawns: []ListRow{}}, err
		}
		var stale *store.CursorMissingReadError
		if finishErr != nil && !errors.As(finishErr, &stale) {
			return ListResult{Spawns: []ListRow{}}, finishErr
		}
		for i, row := range out.Spawns {
			if fallback, ok := fallbacks[row.InstanceID]; ok && run.byID[row.InstanceID].cursorRejected {
				if fallback.err != nil {
					return ListResult{Spawns: []ListRow{}}, fallback.err
				}
				out.Spawns[i] = fallback.row
				// The retained older facts carry their own records, with the
				// rejected terminal write as this read's latest attempt.
				out.Spawns[i].ProcessLiveness, out.Spawns[i].Confirmations = mainFields(run, fallback.stored, filters.Settle)
			}
		}
		return out, nil
	}
	observer, observes := s.(needsIntelligenceStore)
	rows, err := s.ListSpawns(filters)
	if err != nil {
		return ListResult{Spawns: []ListRow{}}, err
	}

	j := newRelayJudge(v)
	out := make([]ListRow, 0, len(rows))
	storeBusy := false
	// Each committed observation's families supersede earlier values, so an
	// ancestor already listed reports a later descendant's exit too.
	families := map[string]string{}
	for _, r := range rows {
		reported := r.State
		if filters.Settle.SettleMs > 0 {
			reported = r.ReportedState
		}
		display := store.DisplayDetail{State: reported}
		var judged *judgedRequestSet
		if observes {
			var read permissionRead
			if r.State == store.StateCheckPermission {
				// Reuse the selected row's checked requests for its projection.
				if read.requests, err = readJudgedRequests(observer.PermissionRequestsForSpawn, j, r.InstanceID); err != nil {
					return ListResult{Spawns: []ListRow{}}, err
				}
			}
			var busy bool
			var committed map[string]string
			display, committed, judged, busy = observeDisplay(s, v, filters.Settle, r, read)
			storeBusy = storeBusy || busy
			for id, family := range committed {
				families[id] = family
			}
		}
		row := listRow(r, reported, display)
		row.ProcessLiveness, row.Confirmations = mainFields(s, r, filters.Settle)
		if r.State == store.StateCheckPermission && judged != nil {
			row.PermissionRequests = judgedOpenInfos(judged, j)
		} else if r.State == store.StateCheckPermission {
			infos, err := openRequestInfos(s.OpenPermissionRequestsForSpawn, j, r.InstanceID)
			if err != nil {
				return ListResult{Spawns: []ListRow{}}, err
			}
			row.PermissionRequests = infos
		}
		out = append(out, row)
	}
	for i := range out {
		if family, ok := families[out[i].InstanceID]; ok {
			out[i].FamilyState = family
		}
	}
	// Every row is built, so the slots below point at their final elements.
	// Once an observation write of this read found the store busy, hook_gone_at
	// is left unwritten without another attempt, as a busy write leaves it.
	if !storeBusy {
		now := j.now()
		var slots []goneSlot
		for i := range out {
			slots = goneSlotsOf(slots, out[i].PermissionRequests, out[i].Agent, out[i].RelayMode, now)
		}
		recordGone(s, now, readerLockWait, slots)
	}
	return ListResult{Spawns: out}, nil
}

// cursorListFallbacks assembles older stored facts before any deferred write.
// A refused proof must not leave its speculative missing state in the result,
// nor can its fallback read queue behind another row's abandoned COMMIT. This
// is response projection only: it does not run recovery or persist fallback
// clocks. A failed optional read matters only if its fallback is needed.
func cursorListFallbacks(run *activeReadStore) map[string]cursorListFallback {
	out := make(map[string]cursorListFallback)
	j := newRelayJudge(run.view)
	for _, proof := range run.cursor {
		checked := run.byID[proof.Row.InstanceID]
		if checked.projectionErr != nil || checked.row.State != store.StateMissing || checked.row.Snapshot != proof.Row.Snapshot {
			// A failed projection already kept its older facts; a changed
			// stored row (including a replacement launch) has its own facts.
			continue
		}
		stored := proof.Row
		// CursorMissingReads projects only State/ReportedState. FamilyState
		// still comes from persisted family history, including independently
		// committed selected-child observations. No projected missing row has
		// yet removed a family contribution; keep that common stored view.
		stored.FamilyState = checked.row.FamilyState
		reported := stored.State
		if run.settle.SettleMs > 0 {
			reported = stored.ReportedState
		}
		display := stored.CommittedDisplay()
		if display.State == StateNeedsIntelligence {
			display = store.DisplayDetail{State: reported}
		}
		row := listRow(stored, reported, display)
		var err error
		if stored.State == store.StateCheckPermission {
			var judged *judgedRequestSet
			judged, err = readJudgedRequests(run.PermissionRequestsForSpawn, j, stored.InstanceID)
			if err == nil {
				row.PermissionRequests = judgedOpenInfos(judged, j)
			}
		}
		out[stored.InstanceID] = cursorListFallback{row, err, stored}
	}
	return out
}

type cursorListFallback struct {
	row    ListRow
	err    error
	stored Spawn
}

// listRow projects only stored row/display facts; request reads stay with the
// caller so ordinary and retained Cursor responses use the same wire mapping.
func listRow(r Spawn, reported string, display store.DisplayDetail) ListRow {
	return ListRow{
		InstanceID:              r.InstanceID,
		ParentID:                r.ParentID,
		State:                   reported,
		DisplayedState:          displayedState(display),
		FamilyState:             r.FamilyState,
		IdleScreenAvailability:  idleScreenAvailability(r.Agent),
		CWD:                     r.CWD,
		Agent:                   r.Agent,
		TmuxSessionName:         r.TmuxSessionName,
		RelayMode:               r.RelayMode,
		Labels:                  r.Labels,
		StartedAt:               r.StartedAt,
		LastSeenAt:              r.LastSeenAt,
		StateChangedAt:          millisToUTC(r.StateChangedMillis),
		EndedAt:                 r.EndedAt,
		LaunchStartedAt:         launchStartedAt(r.State, r.LaunchStartedAtMillis),
		LivenessUnverifiedSince: nullableTimestamp(r.LivenessUnverifiedSince),
		LivenessNote:            nullableString(r.LivenessNote),
		PermissionRequests:      []PermissionRequestInfo{},
	}
}

// List enumerates Spawn rows matching the supplied filter set. All filters AND
// together; an absent filter is permissive. State values OR together. When no
// rows match, ListResult.Spawns is a non-nil empty slice. Returned order is
// unspecified. A pending row also carries launch_started_at, when the agent's
// launch began. A row in check_permission carries its open permission
// requests with their delivery facts, as Get does. With [state] settle_ms
// set, each row's state is its settled state: a hook-written change shows
// only once it has held that long, the last settled state until then. A row
// that shows a Displayed State carries it in displayed_state beside its
// state; entering, leaving or moving between Displayed States instead times
// settle_ms from the read that commits its candidate and shows from a later
// successful read that commits it. State keeps a row whose state or
// displayed_state is one of its values, so --state working lists a working
// row that shows idle_screen. Terminal states and command-driven changes show at once.
// family_state settles independently. The permission requests and
// launch_started_at follow the live state. Stored filters and limit select
// the IDs before external checks. Those same rows are returned after recovery,
// including a selected row whose state changed; no slot is filled from outside
// the selection. Cursor loss is projected against its exact launch/binding
// before deferred persistence. A row-local stale Cursor projection or refused
// write retains that row's earlier stored facts without suppressing other
// selected results or authorizing a terminal write from its rejected proof. Selection-wide
// Cursor projection failures still fail the call. The pass never waits for a
// state change or the store's write lock, and pane failures remain local to the
// selected row.
//
// CLI: agent-director list
//
// Errors:
//   - [ErrInvalidFlags]: Limit is negative; refused before the store is read.
//   - [ErrListInvalidLabel]: a Labels entry is not in "key=value" form.
//
// Nondeterminism: the delivery facts depend on the clock and on whether each
// request's relay hook process runs; with a settle time, so do the states and
// the rows State selects.
func (c *Client) List(params ListParams) (ListResult, error) {
	if err := c.checkClosed(); err != nil {
		return ListResult{Spawns: []ListRow{}}, err
	}
	filters, err := parseListFilters(params, c.settleRead())
	if err != nil {
		return ListResult{Spawns: []ListRow{}}, err
	}
	return listFiltered(c.st, c.relayView(), filters)
}
