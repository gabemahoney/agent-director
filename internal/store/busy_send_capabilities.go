package store

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// The published busy-send capability table (SR-1.6, SR-5.6): per provider, the
// Send Modes it offers, each mode's Message, prompt and text-kind limits, the
// per-agent held limits, its force admission, limits and force-state table
// (SR-4.4), and its Confirm From Transcripts capability (SR-4.5). It is the
// one definition admission, the inbox
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
	// WrapperBytes is the budget for what the route's fixed wrapper adds to
	// one prompt: Wrapping's prefix and suffix stay within it, so a Message
	// at MessageBytes with no text the provider escapes always fits a prompt
	// alone. When no wrapping is recorded, it is also the mode's measure
	// (Measure: the body plus this budget).
	WrapperBytes int64
	// Wrapping is the provider's exact transformation of a prompt body as the
	// agent receives it (Measure); zero for a mode with none recorded.
	Wrapping BusySendWrapping
	TextKind BusySendTextKind
}

// BusySendWrapping is the provider's exact transformation of one prompt body
// as the agent receives it: the fixed text around it and the substitutions
// inside it. Its measure is what the prompt limit counts (SR-1.6, SR-2.3);
// the route that delivers the body and the reader of the agent's own sighting
// of it (SR-3.5) use the same definition.
type BusySendWrapping struct {
	Prefix, Suffix string
	Escapes        []BusySendEscape
}

// BusySendEscape is one substitution the provider makes inside a body: every
// occurrence of Raw is shown as Escaped.
type BusySendEscape struct {
	Raw, Escaped string
}

// Measure is the byte length of body as the agent receives it: Prefix, the
// body with every occurrence of each escape's Raw counted as its Escaped,
// and Suffix.
func (w BusySendWrapping) Measure(body []byte) int64 {
	n := int64(len(w.Prefix) + len(body) + len(w.Suffix))
	for _, e := range w.Escapes {
		n += int64(bytes.Count(body, []byte(e.Raw)) * (len(e.Escaped) - len(e.Raw)))
	}
	return n
}

// zero reports whether no wrapping is recorded.
func (w BusySendWrapping) zero() bool {
	return w.Prefix == "" && w.Suffix == "" && len(w.Escapes) == 0
}

// Measure is the complete size of a prompt with body as the mode's route
// delivers it: Wrapping's exact measure, or the body plus WrapperBytes for a
// mode with no recorded wrapping. It is a BusySendPromptMeasure.
func (l BusySendModeLimits) Measure(body []byte) int64 {
	if l.Wrapping.zero() {
		return int64(len(body)) + l.WrapperBytes
	}
	return l.Wrapping.Measure(body)
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
	// ForceAdmission is the route's qualified admission event for a force's
	// issued prompt (SR-4.3, SR-4.4), "" when the route has none: a force
	// whose issued prompt is unadmitted at its call's end is then failed
	// admission_unknown rather than pending, since no admission can arrive.
	ForceAdmission BusySendAdmissionKind
	// ForceLimits are the named limits of that admission evidence (SR-3.5)
	// and ForceStates the route's force-state table (SR-4.4), each in
	// published order; none without ForceAdmission. They describe measured
	// behaviour only: they grant no issue or admission authority, promise no
	// time and never stand for a force's recorded outcome and reason.
	ForceLimits []BusySendForceLimit
	ForceStates []BusySendForceStateOutcome
	// ConfirmFromTranscripts is what the experimental Confirm From
	// Transcripts setting can add to the provider's force evidence, whatever
	// the setting (SR-4.5), and ConfirmFromTranscriptsReason why ("" for
	// none recorded).
	ConfirmFromTranscripts       BusySendConfirmCapability
	ConfirmFromTranscriptsReason BusySendConfirmReason
}

// BusySendForceLimit is a named limit of a route's force admission evidence
// (SR-3.5), a code get publishes: duplicate unresolved prompt texts bind to
// none, a text whose escaping cannot be undone exactly gives no evidence, and
// a user's own hook that blocks or rewrites the prompt after AD's sighting is
// not detected.
type BusySendForceLimit string

const (
	BusySendForceLimitDuplicateText         BusySendForceLimit = "duplicate_text"
	BusySendForceLimitUnrecoverableEscaping BusySendForceLimit = "unrecoverable_escaping"
	BusySendForceLimitUserHookAfterSighting BusySendForceLimit = "user_hook_after_sighting"
)

// BusySendForceState is the agent's state when a force is made, a code get
// publishes (SR-4.4).
type BusySendForceState string

const (
	BusySendForceStateIdle             BusySendForceState = "idle"
	BusySendForceStateToolRunning      BusySendForceState = "tool_running"
	BusySendForceStateBetweenTools     BusySendForceState = "between_tools"
	BusySendForceStatePermissionDialog BusySendForceState = "permission_dialog"
	BusySendForceStatePlanDialog       BusySendForceState = "plan_dialog"
	BusySendForceStateCompacting       BusySendForceState = "compacting"
	BusySendForceStateNotStarted       BusySendForceState = "not_started"
	BusySendForceStateEnded            BusySendForceState = "ended"
)

// BusySendForceStateOutcome is one row of a route's force-state table
// (SR-4.4): a force made in State, as measured, is handed_over at once,
// pending first (handed_over on the agent's later sighting) or refused with
// its Messages Held. Reason is an existing force reason: the outcome's own for
// handed_over and refused, the wait cause for pending. Any force whose
// sighting arrives after its call returns reports pending, whatever its row.
type BusySendForceStateOutcome struct {
	State   BusySendForceState
	Outcome BusySendForceOutcome
	Reason  BusySendForceReason
}

// BusySendConfirmCapability is whether Confirm From Transcripts can confirm
// a force for a provider (SR-4.5), a code get publishes.
type BusySendConfirmCapability string

const (
	BusySendConfirmCanConfirm    BusySendConfirmCapability = "can_confirm"
	BusySendConfirmNotNeeded     BusySendConfirmCapability = "not_needed"
	BusySendConfirmCannotConfirm BusySendConfirmCapability = "cannot_confirm"
)

// BusySendConfirmReason is why a provider's Confirm From Transcripts
// capability is what it is, a code get publishes. hook_sighting_first: AD's
// own UserPromptSubmit sighting preceded the native input record in every
// measured state, so a transcript match is never a force's last missing
// evidence (Research b.p8z / t1.p8z.9e, Claude Code 2.1.309).
type BusySendConfirmReason string

const BusySendConfirmHookSightingFirst BusySendConfirmReason = "hook_sighting_first"

// Claude Code's Wait Turn limits (E2 decision 4, 2026-11-14), with the
// complete-prompt limit raised to 9,600 bytes by Plan b.xis decision 12 as
// the MI updated it, so a valid Message at the text limit always fits a
// prompt alone (SR-1.6; claudeWaitTurn).
const (
	claudeWaitTurnMessageBytes = 8_000
	claudeWaitTurnPromptBytes  = 9_600
	claudeHeldCount            = 1_000
	claudeHeldBytes            = claudeHeldCount * claudeWaitTurnMessageBytes
)

// claudeWakeWrapping is how Claude Code shows a Wait Turn prompt that route A
// hands over as an asyncRewake FileChanged hook's exit-2 text (Main Agent
// decisions 10 and 12): the measured prefix naming the hook by its event, the
// measured suffix, and its escaping of two reserved closing tags (Research
// b.p8z / t1.p8z.9e, Claude Code 2.1.309). Each escape's result may also be
// literal input, so a body that shows one cannot be undone exactly.
var claudeWakeWrapping = BusySendWrapping{
	Prefix: "<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n<system-reminder>\n" +
		`Stop hook blocking error from command "FileChanged": `,
	Suffix: "\n</system-reminder>",
	Escapes: []BusySendEscape{
		{Raw: "</system-reminder>", Escaped: "&lt;/system-reminder>"},
		{Raw: "</task-notification>", Escaped: `<\/task-notification>`},
	},
}

// claudeWaitTurn is Claude Code's Wait Turn. Its wrapper budget is what a
// Message at the text limit leaves of the prompt after its own separator;
// claudeWakeWrapping's fixed text and its escaping are counted exactly. A
// valid Message at the text limit always fits a prompt alone: its worst case,
// text made entirely of </system-reminder>, renders to 8,000 + 33 separator +
// 169 wrapper + 1,332 escaping = 9,534 bytes, under the 9,600-byte limit.
// Admission's prompt_size refusal stays as a guard against a later change to
// this table; under these figures no valid single Message reaches it.
var claudeWaitTurn = BusySendModeLimits{
	Mode:         BusySendWaitTurn,
	MessageBytes: claudeWaitTurnMessageBytes,
	PromptBytes:  claudeWaitTurnPromptBytes,
	WrapperBytes: claudeWaitTurnPromptBytes - claudeWaitTurnMessageBytes - int64(len(BusySendSeparator(1, 1))),
	Wrapping:     claudeWakeWrapping,
	TextKind:     BusySendTextUTF8TabLF,
}

// claudeForceStates is Claude Code's force-state table (E5 Main Agent
// decision 4): its keyless forced wake reaches the next input boundary in a
// busy, Dialog or compaction state without answering the Dialog, and AD's
// own UserPromptSubmit sighting of the prompt, delayed behind the turn, the
// Dialog or the compaction, admits it (SR-4.4; Research b.p8z / t1.p8z.9e,
// Claude Code 2.1.309). A Launch not yet started or ended refuses.
var claudeForceStates = []BusySendForceStateOutcome{
	{BusySendForceStateIdle, BusySendForceHandedOver, BusySendForceAdmitted},
	{BusySendForceStateToolRunning, BusySendForcePending, BusySendForceTurnRunning},
	{BusySendForceStateBetweenTools, BusySendForcePending, BusySendForceTurnRunning},
	{BusySendForceStatePermissionDialog, BusySendForcePending, BusySendForceDialog},
	{BusySendForceStatePlanDialog, BusySendForcePending, BusySendForceDialog},
	{BusySendForceStateCompacting, BusySendForcePending, BusySendForceCompaction},
	{BusySendForceStateNotStarted, BusySendForceRefused, BusySendForceAgentStarting},
	{BusySendForceStateEnded, BusySendForceRefused, BusySendForceLaunchEnded},
}

// claudeForceLimits are the named limits of Claude's sighting (SR-3.5).
var claudeForceLimits = []BusySendForceLimit{BusySendForceLimitDuplicateText, BusySendForceLimitUnrecoverableEscaping,
	BusySendForceLimitUserHookAfterSighting}

var busySendCapabilityTable = []BusySendCapabilities{{
	Provider:                     AgentClaude,
	HeldCount:                    claudeHeldCount,
	HeldBytes:                    claudeHeldBytes,
	Modes:                        []BusySendModeLimits{claudeWaitTurn},
	ForceAdmission:               BusySendAdmissionPromptSighting,
	ForceLimits:                  claudeForceLimits,
	ForceStates:                  claudeForceStates,
	ConfirmFromTranscripts:       BusySendConfirmNotNeeded,
	ConfirmFromTranscriptsReason: BusySendConfirmHookSightingFirst,
}}

// busySendFallbackCapabilities admits for a covered provider this version has
// no row for, a conversation a later release covered: every version admits and
// holds a covered Launch's Messages whatever its provider (SR-7.2), under
// limits that keep the PRD's corpus and SR-1.6's held minimum. It publishes no
// force route, and this version reads no transcript to confirm a force for it.
var busySendFallbackCapabilities = BusySendCapabilities{
	HeldCount:              claudeHeldCount,
	HeldBytes:              claudeHeldBytes,
	Modes:                  []BusySendModeLimits{busySendFallbackWaitTurn},
	ConfirmFromTranscripts: BusySendConfirmCannotConfirm,
}

// busySendFallbackWaitTurn is Claude's Wait Turn limits without Claude's
// wrapping, which no other provider applies: its whole wrapper budget counts.
var busySendFallbackWaitTurn = func() BusySendModeLimits {
	l := claudeWaitTurn
	l.Wrapping = BusySendWrapping{}
	return l
}()

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
	c.ForceLimits = append([]BusySendForceLimit(nil), c.ForceLimits...)
	c.ForceStates = append([]BusySendForceStateOutcome(nil), c.ForceStates...)
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

// SinglePromptBytes is the complete size of a prompt carrying text alone: its
// separator and text, measured as the mode's route delivers them (Measure).
func (l BusySendModeLimits) SinglePromptBytes(text []byte) int64 {
	return l.Measure(RenderBusySendPromptBody([][]byte{text}))
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
