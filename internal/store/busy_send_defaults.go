package store

// BusySendAgentDefaults are a conversation's Agent Defaults and SR-4.5's
// experimental confirm_from_transcripts setting, as its busy_conversations
// row publishes them (default_send_mode, default_confirmed,
// confirm_from_transcripts). A Message resolves its Send Mode and Confirmed
// Delivery over them (SR-1.4).
type BusySendAgentDefaults struct {
	SendMode               BusySendMode
	Confirmed              bool
	ConfirmFromTranscripts bool
}

// BusySendOutOfBoxDefaults are the values a conversation gets where no launch
// or template chose one: wait_turn, unconfirmed, and confirm_from_transcripts
// off.
var BusySendOutOfBoxDefaults = BusySendAgentDefaults{SendMode: BusySendWaitTurn}

// BusySendDefaultsChoice is what a launch explicitly chose for the Agent
// Defaults, its template's values folded in by the caller: nil chooses
// nothing, so the value the launch would otherwise publish stays; a non-nil
// false or wait_turn is a choice like any other.
type BusySendDefaultsChoice struct {
	SendMode               *BusySendMode
	Confirmed              *bool
	ConfirmFromTranscripts *bool
}

// Over resolves the choice over base: spawn and fresh reuse over
// BusySendOutOfBoxDefaults, resume over the published conversation's values.
func (c BusySendDefaultsChoice) Over(base BusySendAgentDefaults) BusySendAgentDefaults {
	if c.SendMode != nil {
		base.SendMode = *c.SendMode
	}
	if c.Confirmed != nil {
		base.Confirmed = *c.Confirmed
	}
	if c.ConfirmFromTranscripts != nil {
		base.ConfirmFromTranscripts = *c.ConfirmFromTranscripts
	}
	return base
}

// IsZero reports whether the choice chooses nothing.
func (c BusySendDefaultsChoice) IsZero() bool {
	return c.SendMode == nil && c.Confirmed == nil && c.ConfirmFromTranscripts == nil
}
