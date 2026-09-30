package procfix_test

import (
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The fake stays a structural tmux.ProcChecker (Appendix F.2).
var _ tmux.ProcChecker = (*procfix.Checker)(nil)
