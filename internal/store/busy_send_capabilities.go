package store

import (
	"fmt"
	"unicode/utf8"
)

// The published busy-send capability table (SR-1.6, SR-5.6): per provider, the
// Send Modes it offers, each mode's Message, prompt and text-kind limits, and
// the per-agent held limits. It is the one definition admission, the inbox
// coordinator's partitioner, a route's prompt renderer and get's
// delivery_capabilities read; none of them keeps a limit of its own. Live
// qualification (O4, SR-8.3) may raise a limit by changing this table only.
// Every value counts UTF-8 bytes: a byte count is never below the UTF-16 length
// Claude Code counts, so Claude's prompt limit stays under the 10,000-character
// point at which Claude may show a preview in place of hook text (O4). These
// are policy, not a measured maximum.

// BusySendTextKind is the text a mode accepts, a code get publishes.
type BusySendTextKind string

// BusySendTextUTF8TabLF is valid UTF-8 with no NUL and no C0 control
// character other than TAB and LF. A CR is refused, never stripped (SR-1.2).
const BusySendTextUTF8TabLF BusySendTextKind = "utf8_tab_lf"

// BusySendModeLimits are one offered Send Mode's published limits.
type BusySendModeLimits struct {
	Mode BusySendMode
	// MessageBytes is the most bytes of one Message's own text; separators and
	// wrappers do not count (SR-1.6).
	MessageBytes int64
	// PromptBytes is the most bytes of one complete rendered prompt: each
	// Message's separator, the gaps between Messages and the route's wrapper.
	PromptBytes int64
	// WrapperBytes is the most the route's own wrapper adds to one prompt.
	// The route's renderer keeps within it, and admission and the partitioner
	// count all of it, so a Message at MessageBytes always fits a prompt alone.
	WrapperBytes int64
	TextKind     BusySendTextKind
}

// BusySendCapabilities is one provider's row of the table.
type BusySendCapabilities struct {
	Provider string
	// HeldCount and HeldBytes are the per-agent held reservation limits: the
	// Messages, and their text bytes, of accepted custody that can still
	// return to Held (SR-1.6).
	HeldCount int64
	HeldBytes int64
	// Modes are the offered Send Modes, in published order.
	Modes []BusySendModeLimits
}

// Claude Code's Wait Turn limits (Main Agent decision for E2, 2026-11-14).
const (
	claudeWaitTurnMessageBytes = 8_000
	claudeWaitTurnPromptBytes  = 9_000
	claudeHeldCount            = 1_000
	claudeHeldBytes            = claudeHeldCount * claudeWaitTurnMessageBytes
)

// claudeWaitTurn is Claude Code's Wait Turn. Its wrapper budget is what a
// Message at the text limit leaves of the prompt after its own separator.
var claudeWaitTurn = BusySendModeLimits{
	Mode:         BusySendWaitTurn,
	MessageBytes: claudeWaitTurnMessageBytes,
	PromptBytes:  claudeWaitTurnPromptBytes,
	WrapperBytes: claudeWaitTurnPromptBytes - claudeWaitTurnMessageBytes - int64(len(BusySendSeparator(1, 1))),
	TextKind:     BusySendTextUTF8TabLF,
}

var busySendCapabilityTable = []BusySendCapabilities{
	{Provider: AgentClaude, HeldCount: claudeHeldCount, HeldBytes: claudeHeldBytes, Modes: []BusySendModeLimits{claudeWaitTurn}},
}

// busySendFallbackCapabilities admits for a covered provider this version has
// no row for, a conversation a later release covered: every version admits and
// holds a covered Launch's Messages whatever its provider (SR-7.2), under
// limits that keep the PRD's corpus and SR-1.6's held minimum.
var busySendFallbackCapabilities = BusySendCapabilities{
	HeldCount: claudeHeldCount, HeldBytes: claudeHeldBytes, Modes: []BusySendModeLimits{claudeWaitTurn},
}

// BusySendCapabilitiesFor returns provider's row, or the fallback for a
// provider the table does not name. The caller gets its own copy.
func BusySendCapabilitiesFor(provider string) BusySendCapabilities {
	c := busySendFallbackCapabilities
	for _, row := range busySendCapabilityTable {
		if row.Provider == provider {
			c = row
		}
	}
	c.Provider = provider
	c.Modes = append([]BusySendModeLimits(nil), c.Modes...)
	return c
}

// Mode returns mode's limits, and false when the provider does not offer it.
func (c BusySendCapabilities) Mode(mode BusySendMode) (BusySendModeLimits, bool) {
	for _, m := range c.Modes {
		if m.Mode == mode {
			return m, true
		}
	}
	return BusySendModeLimits{}, false
}

// BusySendSeparator is the line AD writes before the nth of a prompt's m
// Messages (SR-2.3); two LF bytes separate one Message from the next.
func BusySendSeparator(n, m int) string {
	return fmt.Sprintf("[agent-director: message %d of %d]\n", n, m)
}

// SinglePromptBytes is the complete size of a prompt carrying one Message of
// textBytes bytes: its separator, its text and the wrapper budget.
func (l BusySendModeLimits) SinglePromptBytes(textBytes int64) int64 {
	return int64(len(BusySendSeparator(1, 1))) + textBytes + l.WrapperBytes
}

// Accepts reports whether text is of the mode's text kind. An unknown kind
// accepts nothing.
func (l BusySendModeLimits) Accepts(text []byte) bool {
	if l.TextKind != BusySendTextUTF8TabLF || !utf8.Valid(text) {
		return false
	}
	for _, b := range text {
		if b < 0x20 && b != '\t' && b != '\n' {
			return false
		}
	}
	return true
}
