package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_config.go holds the shared description helper's case for a
// config file refused for its [tmux] values (SR-4.1) or its [defaults]
// expire_retention_days (b.sgw): ErrConfigMalformed's description names the
// file and states each refused value, then that a missing key, or 0, gives
// the default for every refused key whose default loads. A key whose default
// is below its minimum states its own change that loads instead (b.n4q).

// ConfigRefusal is one refused [tmux] value: Key and its configured Value (0
// for a missing or 0 key whose default is below the minimum); Minimum, the
// safe minimum it is below (0 for a key without one, refused as negative);
// and, for a derived minimum, the effective Create timeout and Pipe-close
// wait it was computed from. When the key's default is below Minimum, Total
// is the effective create_timeout_ms plus pipe_close_wait_ms total the
// description says to lower to (0 when it states none). With Retention set it
// is instead the refused [defaults] expire_retention_days Value, and Key is
// unused.
type ConfigRefusal struct {
	Key          config.TmuxKey
	Value        int64
	Minimum      int64
	Derived      bool
	Create, Pipe int64
	Total        int64
	Retention    bool
}

// DescConfigRefused is ErrConfigMalformed's description for the config file
// at path. With no refusals (a value of the wrong type) only the path is
// required.
func DescConfigRefused(path string, refusals ...ConfigRefusal) DescCase {
	c := DescCase{Name: "config refused", Require: []string{path}}
	var own []string // the refused keys whose default is below their minimum (b.n4q)
	for _, r := range refusals {
		k := r.Key
		var msg string
		switch {
		case r.Retention:
			msg = fmt.Sprintf("[defaults] expire_retention_days = %d, outside its range 1 to %d days",
				r.Value, config.MaxExpireRetentionDays)
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
		if !r.Retention && k.DefaultValue() < r.Minimum {
			msg += fmt.Sprintf(", so set it to at least %d", r.Minimum)
			if r.Total == 0 {
				c.Forbid = append(c.Forbid, msg+", or lower")
			} else {
				msg += fmt.Sprintf(", or lower the effective %s and %s to a total of %d ms or less"+
					" (a missing or 0 key counts as its default)",
					config.TmuxCreateTimeoutMs.Name(), config.TmuxPipeCloseWaitMs.Name(), r.Total)
			}
			own = append(own, "[tmux] "+k.Name())
		}
		c.Require = append(c.Require, msg)
	}
	switch {
	case len(refusals) == 0:
	case len(own) == 0:
		c.Require = append(c.Require, "A missing key, or 0, gives the default.")
		c.MustNot = append(c.MustNot, "so set it to at least")
	case len(own) == len(refusals):
		c.MustNot = append(c.MustNot, "gives the default")
	default:
		c.Require = append(c.Require, "For every refused key other than "+strings.Join(own, " and ")+
			", a missing key, or 0, gives the default.")
	}
	return c
}
