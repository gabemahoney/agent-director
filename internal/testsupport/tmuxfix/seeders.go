// Package tmuxfix — verb-specific Recorder constructors.
//
// These helpers return a *Recorder pre-configured for a specific verb's tmux
// interaction so smoke-test seeders are self-documenting without repeating
// the WithPaneOutput / WithHasSession calls inline.
package tmuxfix

// NewRecorderForReadPane returns a Recorder that answers every CapturePane
// call with sampleText. Use it when the read-pane verb smoke test needs a
// predictable, non-empty pane output to assert against.
func NewRecorderForReadPane(sampleText string) *Recorder {
	return NewRecorder().WithPaneOutput(sampleText)
}

// NewRecorderForResume returns a Recorder whose HasSession always returns
// false — the precondition resume requires so its session-existence check
// does not report ErrTmuxSessionCreate before the new session is launched.
// (HasSession defaults to false already; this constructor documents the
// requirement explicitly so seeders are self-explanatory.)
func NewRecorderForResume() *Recorder {
	return NewRecorder().WithHasSession(false)
}

// NewRecorderForPause returns a Recorder suitable for the pause verb. Pause
// sends one /exit SendKeys call; the recorder's default no-op return is
// sufficient. The note here documents that pause's poll-loop reads the store
// (not the tmux client), so no scripted tmux response is needed to make
// pause terminate — the caller's context deadline controls exit.
func NewRecorderForPause() *Recorder {
	// No special scripted responses needed: pause polls GetSpawnState on the
	// store to detect ended, and the caller's context deadline prevents a hang.
	return NewRecorder()
}
