package tmux

// NewWithRunner builds a client whose socket-taking calls run through r
// instead of the production exec runner. Test-only: tmux_test tests use it.
func NewWithRunner(binary string, t Timeouts, r Runner) *Client {
	c := New(binary, t)
	c.runCall = r
	return c
}

// SocketEnv is a test-built socket-resolution environment: Env holds the set
// variables (a missing key is unset), Wd/WdErr the working directory, UID the
// real uid and DefaultBase the stand-in for /tmp.
type SocketEnv struct {
	Env         map[string]string
	Wd          string
	WdErr       error
	UID         int
	DefaultBase string
}

func (s SocketEnv) seam() socketEnv {
	return socketEnv{
		lookupEnv: func(k string) (string, bool) {
			v, ok := s.Env[k]
			return v, ok
		},
		getwd:       func() (string, error) { return s.Wd, s.WdErr },
		getuid:      func() int { return s.UID },
		defaultBase: s.DefaultBase,
	}
}

// ResolveSocket is ResolveSocket run in s instead of the process.
func (s SocketEnv) ResolveSocket(create bool) (string, error) {
	return s.seam().resolveSocket(create)
}

// EnsureSocketDir is EnsureSocketDir run with s's uid.
func (s SocketEnv) EnsureSocketDir(socket string) error {
	return s.seam().ensureSocketDir(socket)
}
