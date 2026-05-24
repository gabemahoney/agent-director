package api

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SendKeysStore is the narrow store surface SendKeys needs. *store.Store
// satisfies it; tests pass the real store.
type SendKeysStore interface {
	GetSpawn(instanceID string) (Spawn, error)
}

// SendKeysTmux is the narrow tmux surface SendKeys needs. *tmux.Client
// satisfies it; tests pass a recording fake that captures the text +
// press_enter pair without launching real tmux. The tmux client owns
// the literal-text-then-Enter sequencing internally — see
// (*tmux.Client).SendKeys for the wire shape.
type SendKeysTmux interface {
	SendKeys(name, text string, pressEnter bool) error
}

// SendKeysParams is the typed parameter shape for the send-keys verb.
// JSON tags use snake_case so MCP clients can decode into the struct
// directly via the dispatcher's unmarshalSnake helper.
type SendKeysParams struct {
	// ClaudeInstanceID identifies the Spawn whose pane will receive the text.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// Text is the string to deliver to the Spawn's pane. CR bytes (0x0D) are
	// stripped before delivery; LF bytes (0x0A) are preserved as input newlines.
	// A single Enter is always appended to submit the composed buffer.
	Text string `json:"text"`
}

// SendKeysResult is the typed return shape. Empty struct today; reserved
// so future fields (e.g. truncated_count, dropped_cr_count) can be added
// without breaking the JSON wire shape.
type SendKeysResult struct{}

// SendKeys is the verb-handler entry point for `agent-director send-keys`.
// Behavior (SRD §4.3 + reference/send-keys-research.md):
//
//   - `\r` (CR, 0x0D) bytes in Text are STRIPPED before invoking tmux. CR
//     submits the buffer at the position it appears, which would split
//     one logical message into multiple submissions. The fix is per the
//     research note: delete CR bytes from the input.
//   - `\n` (LF, 0x0A) bytes are PRESERVED — Claude's input handler treats
//     LF as "insert newline in input box", not as a submit. Multi-line
//     prompts compose as one message.
//   - A single Enter is always appended via a separate
//     `tmux send-keys -t <name>:0.0 Enter` call after the text. That is
//     the single submit.
//
// State precondition: the Spawn must be in a live, interactive state
// (waiting / working / ask_user / check_permission). pending Spawns have
// not yet booted their TUI; ended / missing Spawns have nothing to type
// into. A non-interactive state surfaces ErrSpawnNotInteractive.
//
// Relay-mode guard: when relay_mode=on AND state=check_permission, the
// permission relay (Epic 10) owns the answer. SendKeys refuses with
// ErrSendKeysWhileRelayed so the relay's decide() write isn't racing a
// pane-side keystroke.
func SendKeys(s SendKeysStore, tmux SendKeysTmux, params SendKeysParams) (SendKeysResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return SendKeysResult{}, err
	}

	if !isInteractiveState(row.State) {
		return SendKeysResult{}, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotInteractive, params.ClaudeInstanceID, row.State)
	}

	if row.RelayMode == "on" && row.State == store.StateCheckPermission {
		return SendKeysResult{}, fmt.Errorf("%w: spawn %s is awaiting a relayed permission decision",
			ErrSendKeysWhileRelayed, params.ClaudeInstanceID)
	}

	cleaned := strings.ReplaceAll(params.Text, "\r", "")
	// The tmux client handles the literal-text-then-real-Enter split
	// internally (see (*tmux.Client).SendKeys); the verb hands it the
	// cleaned text plus pressEnter=true.
	if err := tmux.SendKeys(row.TmuxSessionName, cleaned, true); err != nil {
		return SendKeysResult{}, err
	}

	return SendKeysResult{}, nil
}

// isInteractiveState returns true iff the supplied state value belongs to
// the set of live conversational states send-keys is allowed to drive.
// pending is excluded because the TUI isn't up yet — the first
// SessionStart hook flips pending to waiting, after which the Spawn is
// reachable.
func isInteractiveState(state string) bool {
	switch state {
	case store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission:
		return true
	}
	return false
}

// SendKeys sends text into a tracked Spawn's tmux pane. CR bytes (0x0D) are
// stripped before delivery to prevent premature submission; LF bytes (0x0A)
// are preserved as composed newlines in Claude's input box. A single Enter is
// always appended to submit the composed buffer.
//
// CLI: agent-director send-keys
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotInteractive]: the Spawn's state is not one of waiting,
//     working, ask_user, or check_permission.
//   - [ErrSendKeysWhileRelayed]: relay_mode is on and state is
//     check_permission; the relay path owns the modal answer.
//   - ErrTmuxNotAvailable: tmux binary is not on PATH.
//   - ErrTmuxSendKeys: tmux send-keys exited non-zero.
//
// Nondeterminism: none.
func (c *Client) SendKeys(params SendKeysParams) (SendKeysResult, error) {
	if err := c.checkClosed(); err != nil {
		return SendKeysResult{}, err
	}
	return SendKeys(c.st, c.tmuxClient, params)
}
