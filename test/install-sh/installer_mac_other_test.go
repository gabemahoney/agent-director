//go:build !darwin

package installsh_test

// SR-9's installer cases off the Mac: moveMacCase only runs install.sh, so
// TestAgentCodexOnly and TestMoveSuccessful/plain run and report exactly as
// they always have. The Mac's tool record and failure report are
// installer_mac_darwin_test.go's, with the same API.

import "testing"

// moveMacCase is one of SR-9's installer cases.
type moveMacCase struct {
	t *testing.T
	h *moveHome
}

// newMoveMacCase records nothing off the Mac; behaviour names the case there.
func newMoveMacCase(t *testing.T, h *moveHome, behaviour string) *moveMacCase {
	return &moveMacCase{t: t, h: h}
}

// Install runs install.sh as h.Install does.
func (c *moveMacCase) Install(args ...string) moveResult {
	c.t.Helper()
	return c.h.Install(args...)
}

// WantSidecars checks nothing off the Mac.
func (c *moveMacCase) WantSidecars(db string) {}
