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

// callerIdentity collects the invoking process's identity once per call.
// Shared by Client.Decide, Kill, send-keys (sendKeys) and Pause so the trail
// emit fields stay identical across those surfaces.
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

// lazyCaller is a sweep run's caller identity, collected inside agent-director
// (callerIdentity) on first use and shared by every trail record of the run,
// so a run collects it at most once and a run that writes no record carrying
// it collects none. find-missing and expire each hold one per run. The zero
// value is ready to use; it serves one run and is not for concurrent use.
type lazyCaller struct {
	c         caller
	collected bool
}

// get returns the run's caller identity, collecting it on the first call.
func (l *lazyCaller) get() caller {
	if !l.collected {
		l.c, l.collected = callerIdentity(), true
	}
	return l.c
}
