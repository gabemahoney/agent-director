package api

import (
	"os"
	"os/user"
	"path/filepath"
)

// caller describes the identity of the process invoking an AD Client method.
// It is collected from inside AD (os/user APIs), never from caller-asserted
// params (SR-A-2.4 / SR-5.2), and emitted on the trail as caller_* fields.
type caller struct {
	process  string
	pid      int
	hostname string
	user     string
}

// callerIdentity collects the invoking process's identity once at method entry.
// Shared by Client.Decide and Client.SendKeys so the trail emit fields stay
// identical across both surfaces.
func callerIdentity() caller {
	c := caller{
		process: filepath.Base(os.Args[0]),
		pid:     os.Getpid(),
	}
	c.hostname, _ = os.Hostname()
	if u, uerr := user.Current(); uerr == nil {
		c.user = u.Username
	}
	return c
}
