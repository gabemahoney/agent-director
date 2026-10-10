package api

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// ReadPaneStore is the narrow store surface ReadPane needs (SRD Appendix
// F.3): the row read and this store's id, which every label the lookup
// accepts ends with (SR-3.4; WD 2026-09-29 STORE). It has no write: read-pane
// changes nothing (SR-7.5). *store.Store satisfies it.
type ReadPaneStore interface {
	// GetSpawn reads the row; an unknown id is ErrSpawnNotFound.
	GetSpawn(instanceID string) (Spawn, error)
	// StoreID returns this store's store_meta.store_id.
	StoreID() string
}

// ReadPaneTmux is the narrow tmux surface ReadPane needs (Appendix F.3): the
// lookup, the pane listing and the capture of one pane by its pane id.
// TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy it. Every method
// takes the row's socket (SR-3.3) and reports a failure as *TmuxCallError.
type ReadPaneTmux interface {
	TmuxLookup
	// ListPanes lists every pane of the server at socket.
	ListPanes(socket string) ([]TmuxPane, error)
	// CapturePaneID returns the last nLines lines of the pane paneID on
	// socket; ansi keeps the escape sequences (tmux's -e flag).
	CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error)
}

// The production types satisfy ReadPane's interfaces.
var (
	_ ReadPaneStore = (*store.Store)(nil)
	_ ReadPaneTmux  = TmuxClient(nil)
)

// DefaultReadPaneLines is the SRD §12 default for n_lines when the caller
// passes 0 (or omits the parameter). Pinned at the package level so the
// CLI flag default and the MCP tool schema reference the same number.
const DefaultReadPaneLines = 25

// ReadPaneParams is the typed parameter shape for the read-pane verb.
// NLines=0 falls back to DefaultReadPaneLines so an MCP caller that
// omits the field gets the documented default. There is no upper cap
// (SRD §12 explicitly leaves the bound to the caller); a negative NLines is
// refused with ErrInvalidFlags.
type ReadPaneParams struct {
	// ClaudeInstanceID identifies the Spawn whose pane will be captured.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// NLines is the number of trailing pane lines to return. 0 falls back to
	// [DefaultReadPaneLines] (25). There is no upper cap. A negative value is
	// refused with [ErrInvalidFlags].
	NLines int `json:"n_lines"`
	// ANSI controls ANSI escape handling. When false (default) escape sequences
	// are stripped while unicode TUI glyphs are preserved. When true raw bytes
	// from tmux capture-pane -e are returned verbatim.
	ANSI bool `json:"ansi"`
	// AllowPending is accepted for surface symmetry with send-keys. ReadPane
	// has no state guard (any state including pending/ended/missing is always
	// readable), so this field has no behavioral effect today.
	AllowPending bool `json:"allow_pending"`
}

// ReadPaneResult is the typed return shape — a single `pane` string field
// the CLI marshals to `{"pane":"..."}`. Keeping the payload behind one
// key leaves room for future fields (e.g. `truncated`, `state_at_capture`)
// without breaking the wire shape.
type ReadPaneResult struct {
	// Pane is the captured pane text. ANSI handling depends on ReadPaneParams.ANSI.
	Pane string `json:"pane"`
}

// ReadPane is the verb-handler entry point for `agent-director read-pane`
// (SRD SR-7.1, SR-7.2, SR-7.3, SR-7.5, SR-3.7). It reads the agent's own
// pane, or a lone leftover's, and changes nothing:
//
//   - A negative NLines: ErrInvalidFlags, before the row is read (b.c4n).
//   - Unknown id: ErrSpawnNotFound. There is no state guard: a pending,
//     live or finished row behaves alike, and a finished row whose own
//     session runs returns its pane.
//   - A row whose recorded name is unusable (SR-3.2): ErrInternal, no tmux
//     call.
//   - Then the row's socket (SR-3.3; a resolution refusal is
//     ErrTmuxNotAvailable) and one lookup by the row's current label.
//   - Ours: one pane listing, then the agent's pane (the entry with the
//     row's recorded pane id and pid, wherever it now is) is captured by its
//     pane id. For a row that records no pane (a lost create reply), the
//     one pane whose @ad_pane names the row's launch token is used for this
//     call only, with no write (SR-3.6). No such pane: ErrTmuxSessionConflict
//     ("the agent's pane was not found"), nothing read.
//   - Leftover: with exactly one leftover session of this store, one pane
//     listing, then the pane whose @ad_pane names that leftover's launch
//     token is captured by its pane id; no such pane is the same
//     ErrTmuxSessionConflict. More than one leftover: ErrTmuxSessionConflict,
//     with no listing and nothing read.
//   - Gone (another agent-director store's sessions included): the gone
//     error ErrTmuxCaptureFailed ("the row's session is not there"), no
//     listing, nothing read.
//   - Can't tell, tmux unavailable, and a pane listing that fails other than
//     by showing no server: the single-row verbs' shared mapping
//     (ErrTmuxNotAvailable, ErrTmuxSessionConflict "conflicting labels",
//     ErrTmuxUnresponsive), saying "nothing was done" as kill's do. A
//     listing that shows no server is Gone.
//   - A failed capture: a timeout is ErrTmuxUnresponsive with no further
//     call; any other failure makes one follow-up lookup, whose Gone or
//     Leftover gives ErrTmuxCaptureFailed and whose other outcomes give
//     ErrTmuxUnresponsive, or ErrTmuxNotAvailable for a different server or
//     tmux unavailable (SR-7.3); these say "nothing was done".
//
// Only the pane refusals (the agent's pane not found, Leftover, the row's
// session not there) say "nothing was read" (SR-1.4).
//
// Every tmux call uses the row's socket, and the capture targets a pane id,
// never a session name or a pane index. The calls are at most one lookup,
// one pane listing, one capture and, only after a capture failure other than
// a timeout, one follow-up lookup (SR-13.2). ReadPane writes nothing to the
// store and emits no trail event on any path.
//
// Output (SRD §12 + reference/pane-output-research.md):
//
//   - NLines=0 → DefaultReadPaneLines (25). No upper cap; callers asking
//     for "all available scrollback" pass a large number themselves.
//     NLines counts lines of history before the visible pane (-S -<n>). A
//     negative NLines is refused, never clamped.
//   - ANSI=false (default) → strip ANSI escape sequences but preserve
//     unicode TUI glyphs (❯, ⎿, 🐝, box-drawing). The caller reads glyphs
//     as state signal; ASCII-mapping them would destroy that.
//   - ANSI=true → return raw bytes from tmux exactly as captured. Useful
//     for a TUI viewer or a debugger inspecting color-coded output.
//
// ReadPane judges the lookup's server with the production start-time reader
// (probe.NewProcChecker), the one [New] gives a Client.
func ReadPane(s ReadPaneStore, t ReadPaneTmux, params ReadPaneParams) (ReadPaneResult, error) {
	return readPane(s, t, probe.NewProcChecker(), params)
}

// readPane is ReadPane with the start-time reader pc, which judges the
// lookup's and the follow-up's server (SR-3.3) and a pane taken for a lost
// create reply (SR-3.6). Client.ReadPane passes the Client's own reader.
func readPane(s ReadPaneStore, t ReadPaneTmux, pc ProcChecker, params ReadPaneParams) (ReadPaneResult, error) {
	if params.NLines < 0 {
		return ReadPaneResult{}, fmt.Errorf("%w: n_lines = %d is negative; pass 0 (or omit it) for the default of %d lines, or a positive number of lines",
			ErrInvalidFlags, params.NLines, DefaultReadPaneLines)
	}
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return ReadPaneResult{}, err
	}
	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return ReadPaneResult{}, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	socket, err := rowSocket(row.Identity.Socket)
	if err != nil {
		return ReadPaneResult{}, fmt.Errorf("instance %s: %w", row.ClaudeInstanceID, err)
	}
	r := &readPaneRun{paneRun{
		t: t, pc: pc, row: row, storeID: s.StoreID(), socket: socket,
		gone: tmux.ErrTmuxCaptureFailed, nothing: nothingRead,
	}}

	paneID, launch, err := r.target()
	if err != nil {
		return ReadPaneResult{}, err
	}

	n := params.NLines
	if n == 0 {
		n = DefaultReadPaneLines
	}
	pane, err := t.CapturePaneID(socket, paneID, n, params.ANSI)
	if err != nil {
		_, err = paneActionFailureError(err, paneActionFailure{
			Call:    tmux.CallCapture,
			Gone:    tmux.ErrTmuxCaptureFailed,
			Pane:    r.refusal(row.TmuxSessionName),
			Refusal: r.cantTellRefusal(tmux.CallCapture),
		}, t, pc, launch)
		return ReadPaneResult{}, err
	}

	if !params.ANSI {
		pane = tmux.StripANSI(pane)
	}
	return ReadPaneResult{Pane: pane}, nil
}

// readPaneRun is one ReadPane call: the pane verbs' shared run with
// read-pane's gone sentinel and "nothing was read", and no adoption write.
type readPaneRun struct {
	paneRun
}

// target makes the lookup and, when it needs one, the pane listing, and
// returns the pane id to capture with the launch view a capture failure's
// follow-up lookup uses, or the verb error, with nothing read.
func (r *readPaneRun) target() (string, tmux.Launch, error) {
	launch := r.launchFor(r.row.Identity)
	res := tmux.Lookup(r.t, r.pc, launch, "")
	switch res.Verdict {
	case tmux.Ours:
		return r.ours(res, launch)
	case tmux.Leftover:
		return r.leftover(res, launch)
	case tmux.Gone:
		return "", launch, r.goneError()
	}
	return "", launch, cantTellError(res, r.cantTellRefusal(tmux.CallLookup))
}

// leftover reads a lone leftover's pane (SR-3.7, SR-7.2): with exactly one
// leftover session, the pane whose @ad_pane names its label's token, from
// one pane listing. More than one leftover is refused before any listing.
// res.Leftovers holds only this store's sessions.
func (r *readPaneRun) leftover(res tmux.Result, launch tmux.Launch) (string, tmux.Launch, error) {
	if len(res.Leftovers) != 1 {
		return "", launch, paneLeftoverError(r.refusal(""), res.Leftovers, true)
	}
	lone := res.Leftovers[0]
	panes, err := r.listPanes(launch)
	if err != nil {
		return "", launch, err
	}
	pane, match := tmux.PaneByToken(panes, lone.Label.Token)
	if match != tmux.PaneOne {
		return "", launch, paneNotFoundError(r.refusal(lone.Name), false)
	}
	return pane.ID, launch, nil
}

// ReadPane captures the last N lines of the agent's own pane: the row's pane,
// found by the row's label on its recorded socket and targeted by pane id.
// When NLines is 0 the default of [DefaultReadPaneLines] (25) is used; there
// is no upper cap, and a negative NLines is refused. By default ANSI escape
// sequences are stripped while unicode TUI glyphs are preserved; set
// ANSI:true to receive raw bytes.
//
// The pane read is the agent's own pane in the tmux session that carries the
// row's current launch label, on the row's recorded socket, found by its pane
// id and pid; with no session of the current launch, it is the pane of the
// one session an earlier launch of this row left behind, found by that
// launch's pane label. ReadPane reads nothing from another row's session, an
// unlabelled session or another agent-director store's session.
//
// ReadPane changes nothing: it sends no keys, writes nothing to the store
// (the row's row_version and snapshot are unchanged) and emits no trail
// event. It is not a liveness verb; the row's state stays the liveness
// authority. Unlike SendKeys, ReadPane has no state precondition: any state
// (including pending and ended/missing) is readable as-is. AllowPending is
// accepted for surface symmetry with send-keys but has no behavioral effect
// here.
//
// A row in any state whose recorded tmux session name cannot be used (it is
// empty, contains a control character, or contains a character tmux stores
// differently) gets ErrInternal (an error matching no catalogued sentinel)
// with no tmux call; removing the row is a human's decision (see "Operator
// actions" in the agent-director README).
//
// CLI: agent-director read-pane
//
// Errors:
//   - [ErrInvalidFlags]: NLines is negative; refused before the row is read.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on; nothing was done.
//   - [ErrTmuxCaptureFailed]: the row's tmux session is not there; nothing
//     was read.
//   - [ErrTmuxUnresponsive]: tmux did not answer, or gave a reply that could
//     not be recognised; nothing was done.
//   - [ErrTmuxSessionConflict]: the agent's pane was not found, or more than
//     one session an earlier launch left behind is there (nothing was read);
//     or tmux holds conflicting labels (nothing was done).
//
// Nondeterminism: none.
func (c *Client) ReadPane(params ReadPaneParams) (ReadPaneResult, error) {
	if err := c.checkClosed(); err != nil {
		return ReadPaneResult{}, err
	}
	return readPane(c.st, c.tmuxClient, c.procChecker, params)
}
