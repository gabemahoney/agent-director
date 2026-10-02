package apitest

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_config.go holds the shared description helper's case for a
// config file refused for its [tmux] values (SR-4.1): ErrConfigMalformed's
// description names the file and states each refused value, then that a
// missing key, or 0, gives the default.

// ConfigRefusal is one refused [tmux] value: Key and its configured Value (0
// for a missing or 0 key whose default is below the minimum); Minimum, the
// safe minimum it is below (0 for a key without one, refused as negative);
// and, for a derived minimum, the effective Create timeout and Pipe-close
// wait it was computed from.
type ConfigRefusal struct {
	Key          config.TmuxKey
	Value        int64
	Minimum      int64
	Derived      bool
	Create, Pipe int64
}

// DescConfigRefused is ErrConfigMalformed's description for the config file
// at path. With no refusals (a value of the wrong type) only the path is
// required.
func DescConfigRefused(path string, refusals ...ConfigRefusal) DescCase {
	c := DescCase{Name: "config refused", Require: []string{path}}
	for _, r := range refusals {
		k := r.Key
		var msg string
		switch {
		case r.Minimum == 0:
			msg = fmt.Sprintf("[tmux] %s = %d, which must be positive", k.Name(), r.Value)
		case r.Value == 0:
			msg = fmt.Sprintf("[tmux] %s is missing or 0, and its default, %d, is below its safe minimum %d %s",
				k.Name(), k.DefaultValue(), r.Minimum, k.Unit())
		default:
			msg = fmt.Sprintf("[tmux] %s = %d, below its safe minimum %d %s", k.Name(), r.Value, r.Minimum, k.Unit())
		}
		if r.Derived {
			msg += fmt.Sprintf(" (computed from the effective %s %d and %s %d)",
				config.TmuxCreateTimeoutMs.Name(), r.Create, config.TmuxPipeCloseWaitMs.Name(), r.Pipe)
		}
		c.Require = append(c.Require, msg)
	}
	if len(refusals) > 0 {
		c.Require = append(c.Require, "A missing key, or 0, gives the default.")
	}
	return c
}
