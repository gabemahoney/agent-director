package api

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/claude-director/internal/store"
)

// ErrListInvalidLabel is returned when a caller-supplied label filter
// cannot be parsed as `key=value`. The error name is stable so the
// CLI envelope can pin it; the description carries the offending raw
// input.
var ErrListInvalidLabel = errors.New("ErrListInvalidLabel")

// ListStore is the narrow store surface List needs. *store.Store
// satisfies it via ListSpawns; tests fake the surface so the verb's
// filter-translation can be exercised without driving SQLite.
type ListStore interface {
	ListSpawns(f store.ListFilters) ([]store.Spawn, error)
}

// ListParams is the typed parameter shape for the list verb.
// State accepts a comma-separated slice from the CLI parser ("a,b"
// becomes []string{"a","b"}). Labels arrives as a flat slice of
// "k=v" strings — List parses it into the store's typed filter map
// so the JSON / CLI / MCP surfaces share one validation seam.
type ListParams struct {
	State  []string
	Labels []string
	Parent string
	Cwd    string
	Limit  int
}

// ListRow mirrors a returned Spawn at the JSON wire level. The store
// returns a richer struct; the verb projects only the fields the SRD
// §12 list shape promises so future store-internal additions don't
// leak silently.
type ListRow struct {
	ClaudeInstanceID string            `json:"claude_instance_id"`
	ParentID         string            `json:"parent_id,omitempty"`
	State            string            `json:"state"`
	CWD              string            `json:"cwd"`
	TmuxSessionName  string            `json:"tmux_session_name"`
	RelayMode        string            `json:"relay_mode"`
	Labels           map[string]string `json:"labels"`
	StartedAt        time.Time         `json:"started_at"`
	LastSeenAt       time.Time         `json:"last_seen_at"`
	EndedAt          *time.Time        `json:"ended_at,omitempty"`
}

// ListResult is the typed return shape. Spawns is always a non-nil
// slice (possibly empty) so the JSON envelope encodes as `[]`, never
// `null`.
type ListResult struct {
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

	rows, err := s.ListSpawns(store.ListFilters{
		State:  params.State,
		Labels: labels,
		Parent: params.Parent,
		Cwd:    params.Cwd,
		Limit:  params.Limit,
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
