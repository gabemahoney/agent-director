package api

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrListInvalidLabel is returned by the list verb when a caller-supplied
// label filter cannot be parsed as `key=value`. The error name is stable so
// the CLI envelope can pin it; the description carries the offending raw input.
var ErrListInvalidLabel = errors.New("ErrListInvalidLabel")

// ListStore is the narrow store surface List needs. *store.Store
// satisfies it via ListSpawns; tests fake the surface so the verb's
// filter-translation can be exercised without driving SQLite.
type ListStore interface {
	ListSpawns(f ListFilters) ([]Spawn, error)
}

// ListParams is the typed parameter shape for the list verb.
// State accepts a comma-separated slice from the CLI parser ("a,b"
// becomes []string{"a","b"}). Labels arrives as a flat slice of
// "k=v" strings — List parses it into the store's typed filter map
// so the JSON / CLI / MCP surfaces share one validation seam.
type ListParams struct {
	// State filters by lifecycle state. Multiple values OR together.
	// Nil/empty means no state filter (all states returned).
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
	// Limit caps the number of rows returned. 0 means no cap.
	Limit int
}

// ListRow mirrors a returned Spawn at the JSON wire level. The store
// returns a richer struct; the verb projects only the fields the SRD
// §12 list shape promises so future store-internal additions don't
// leak silently.
type ListRow struct {
	// ClaudeInstanceID is the stable id of the Spawn.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// ParentID is the id of the spawning Spawn. Omitted from JSON when empty.
	ParentID string `json:"parent_id,omitempty"`
	// State is the current lifecycle state.
	State string `json:"state"`
	// CWD is the canonicalized working directory the Spawn was started in.
	CWD string `json:"cwd"`
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
	// EndedAt is set when state moves to ended. Omitted from JSON while live.
	EndedAt *time.Time `json:"ended_at,omitempty"`
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
// Validation: every Labels entry must contain a literal `=`. A label
// of the form `foo` (no separator) yields ErrListInvalidLabel; the
// store is never reached. Empty key (`=v`) is also rejected so the
// json_extract path never receives an empty key string.
func List(s ListStore, params ListParams) (ListResult, error) {
	labels := make(map[string]string, len(params.Labels))
	for _, raw := range params.Labels {
		idx := strings.IndexByte(raw, '=')
		if idx <= 0 {
			return ListResult{}, fmt.Errorf("%w: %q is not in key=value form", ErrListInvalidLabel, raw)
		}
		labels[raw[:idx]] = raw[idx+1:]
	}

	rows, err := s.ListSpawns(ListFilters{
		State:           params.State,
		Labels:          labels,
		Parent:          params.Parent,
		Cwd:             params.Cwd,
		TmuxSessionName: params.TmuxSessionName,
		Limit:           params.Limit,
	})
	if err != nil {
		return ListResult{}, err
	}

	out := make([]ListRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ListRow{
			ClaudeInstanceID: r.ClaudeInstanceID,
			ParentID:         r.ParentID,
			State:            r.State,
			CWD:              r.CWD,
			TmuxSessionName:  r.TmuxSessionName,
			RelayMode:        r.RelayMode,
			Labels:           r.Labels,
			StartedAt:        r.StartedAt,
			LastSeenAt:       r.LastSeenAt,
			EndedAt:          r.EndedAt,
		})
	}
	return ListResult{Spawns: out}, nil
}

// List enumerates Spawn rows matching the supplied filter set. All filters AND
// together; an absent filter is permissive. State values OR together. When no
// rows match, ListResult.Spawns is a non-nil empty slice. Returned order is
// unspecified.
//
// CLI: agent-director list
//
// Errors:
//   - [ErrListInvalidLabel]: a Labels entry is not in "key=value" form.
//
// Nondeterminism: none.
func (c *Client) List(params ListParams) (ListResult, error) {
	if err := c.checkClosed(); err != nil {
		return ListResult{}, err
	}
	return List(c.st, params)
}
