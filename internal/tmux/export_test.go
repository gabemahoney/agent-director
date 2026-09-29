package tmux

// NewWithRunner builds a client whose socket-taking calls run through r
// instead of the production exec runner. Test-only: tmux_test tests use it.
func NewWithRunner(binary string, t Timeouts, r Runner) *Client {
	c := New(binary, t)
	c.runCall = r
	return c
}
