package api

import (
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// ReadPaneStore is the narrow store surface ReadPane needs. *store.Store
// satisfies it; tests pass the real store.
type ReadPaneStore interface {
	GetSpawn(instanceID string) (Spawn, error)
}

// ReadPaneTmux is the narrow tmux surface ReadPane needs. *tmux.Client
// satisfies it; tests pass a recording fake that returns scripted bytes.
// The ansi parameter forwards to tmux's `-e` flag — see CapturePane.
type ReadPaneTmux interface {
	CapturePane(name string, nLines int, ansi bool) (string, error)
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
	// ClaudeInstanceID identifies the Spawn whose pane will be captured.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// NLines is the number of trailing pane lines to return. 0 falls back to
	// [DefaultReadPaneLines] (25). There is no upper cap.
	NLines int `json:"n_lines"`
	// ANSI controls ANSI escape handling. When false (default) escape sequences
	// are stripped while unicode TUI glyphs are preserved. When true raw bytes
	// from tmux capture-pane -e are returned verbatim.
	ANSI bool `json:"ansi"`
}

// ReadPaneResult is the typed return shape — a single `pane` string field
// the CLI marshals to `{"pane":"..."}`. Keeping the payload behind one
// key leaves room for future fields (e.g. `truncated`, `state_at_capture`)
// without breaking the wire shape.
type ReadPaneResult struct {
	// Pane is the captured pane text. ANSI handling depends on ReadPaneParams.ANSI.
	Pane string `json:"pane"`
}

// ReadPane is the verb-handler entry point for `agent-director read-pane`.
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
func ReadPane(s ReadPaneStore, t ReadPaneTmux, params ReadPaneParams) (ReadPaneResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return ReadPaneResult{}, err
	}

	n := params.NLines
	if n <= 0 {
		n = DefaultReadPaneLines
	}

	pane, err := t.CapturePane(row.TmuxSessionName, n, params.ANSI)
	if err != nil {
		return ReadPaneResult{}, err
	}

	if !params.ANSI {
		pane = tmux.StripANSI(pane)
	}
	return ReadPaneResult{Pane: pane}, nil
}

// ReadPane captures the last N lines of a tracked Spawn's tmux pane. When
// NLines is 0 the default of [DefaultReadPaneLines] (25) is used; there is no
// upper cap. By default ANSI escape sequences are stripped while unicode TUI
// glyphs are preserved; set ANSI:true to receive raw bytes.
//
// Unlike SendKeys, ReadPane has no state precondition: an ended or missing
// Spawn's final pane contents can be read as a post-mortem.
//
// CLI: agent-director read-pane
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - ErrTmuxNotAvailable: tmux binary is not on PATH.
//   - ErrTmuxCaptureFailed: tmux capture-pane failed (session may have vanished).
//
// Nondeterminism: none.
func (c *Client) ReadPane(params ReadPaneParams) (ReadPaneResult, error) {
	if err := c.checkClosed(); err != nil {
		return ReadPaneResult{}, err
	}
	return ReadPane(c.st, c.tmuxClient, params)
}
