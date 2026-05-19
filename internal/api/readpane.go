package api

import (
	"github.com/gabemahoney/claude-director/internal/store"
	"github.com/gabemahoney/claude-director/internal/tmux"
)

// ReadPaneTmux is the narrow tmux surface ReadPane needs. *tmux.Client
// satisfies it; tests pass a recording fake that returns scripted bytes.
type ReadPaneTmux interface {
	CapturePane(name string, nLines int) (string, error)
}

// DefaultReadPaneLines is the SRD §12 default for n_lines when the caller
// passes 0 (or omits the parameter). Pinned at the package level so the
// CLI flag default and the MCP tool schema reference the same number.
const DefaultReadPaneLines = 25

// ReadPaneParams is the typed parameter shape for the read-pane verb.
// NLines=0 falls back to DefaultReadPaneLines so an MCP caller that
// omits the field gets the documented default. There is no upper cap
// (SRD §12 explicitly leaves the bound to the caller).
type ReadPaneParams struct {
	ClaudeInstanceID string
	NLines           int
	ANSI             bool
}

// ReadPaneResult is the typed return shape — a single `pane` string field
// the CLI marshals to `{"pane":"..."}`. Keeping the payload behind one
// key leaves room for future fields (e.g. `truncated`, `state_at_capture`)
// without breaking the wire shape.
type ReadPaneResult struct {
	Pane string `json:"pane"`
}

// ReadPane is the verb-handler entry point for `claude-director read-pane`.
// Behavior (SRD §12 + reference/pane-output-research.md):
//
//   - NLines=0 → DefaultReadPaneLines (25). No upper cap; callers asking
//     for "all available scrollback" pass a large number themselves.
//   - ANSI=false (default) → strip ANSI escape sequences but preserve
//     unicode TUI glyphs (❯, ⎿, 🐝, box-drawing). The orchestrator reads
//     glyphs as state signal; ASCII-mapping them would destroy that.
//   - ANSI=true → return raw bytes from tmux exactly as captured. Useful
//     for a TUI viewer or a debugger inspecting color-coded output.
//
// Errors: ErrSpawnNotFound when the id is unknown; ErrTmuxCaptureFailed
// for transport-layer tmux failures (e.g. the session vanished between
// the row lookup and the capture call).
func ReadPane(s *store.Store, t ReadPaneTmux, params ReadPaneParams) (ReadPaneResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return ReadPaneResult{}, err
	}

	n := params.NLines
	if n <= 0 {
		n = DefaultReadPaneLines
	}

	pane, err := t.CapturePane(row.TmuxSessionName, n)
	if err != nil {
		return ReadPaneResult{}, err
	}

	if !params.ANSI {
		pane = tmux.StripANSI(pane)
	}
	return ReadPaneResult{Pane: pane}, nil
}
