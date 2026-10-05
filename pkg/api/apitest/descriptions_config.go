package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_config.go holds the shared description helper's case for a
// config file refused for its [tmux] values (SR-4.1), its [defaults]
// expire_retention_days (b.sgw) or its [relay] or [pause] timeout_seconds
// (b.8q2): ErrConfigMalformed's description names the file and lists the
// refused tables, states each refused value, then that a missing key, or 0,
// gives the default for every refused key whose default loads. A key whose
// default is below its minimum states its own change that loads instead
// (b.n4q).

// ConfigRefusal is one refused [tmux] value: Key and its configured Value (0
// for a missing or 0 key whose default is below the minimum); Minimum, the
// safe minimum it is below (0 for a key without one, refused as negative);
// and, for a derived minimum, the effective Create timeout and Pipe-close
// wait it was computed from. When the key's default is below Minimum, Total
// is the effective create_timeout_ms plus pipe_close_wait_ms total the
// description says to lower to (0 when it states none). With Retention set it
// is instead the refused [defaults] expire_retention_days Value, and with
// RelayTimeout or PauseTimeout set the refused [relay] or [pause]
// timeout_seconds Value (b.8q2); Key is then unused.
type ConfigRefusal struct {
	Key          config.TmuxKey
	Value        int64
	Minimum      int64
	Derived      bool
	Create, Pipe int64
	Total        int64
	Retention    bool
	RelayTimeout bool
	PauseTimeout bool
}

// configTables are the tables a refusal description lists, in its order.
var configTables = []string{"[defaults]", "[relay]", "[pause]", "[tmux]"}

// table is the table r's key is in.
func (r ConfigRefusal) table() string {
	switch {
	case r.Retention:
		return "[defaults]"
	case r.RelayTimeout:
		return "[relay]"
	case r.PauseTimeout:
		return "[pause]"
	}
	return "[tmux]"
}

// configHeader is the description's "refused <tables> values: " for refusals:
// one table alone, two joined by " and ", more separated by ", " with " and "
// before the last.
func configHeader(refusals []ConfigRefusal) string {
	var tables []string
	for _, tb := range configTables {
		for _, r := range refusals {
			if r.table() == tb {
				tables = append(tables, tb)
				break
			}
		}
	}
	list := tables[len(tables)-1]
	if len(tables) > 1 {
		list = strings.Join(tables[:len(tables)-1], ", ") + " and " + list
	}
	return "refused " + list + " values: "
}

// DescConfigRefused is ErrConfigMalformed's description for the config file
// at path. With no refusals (a value of the wrong type) only the path is
// required.
func DescConfigRefused(path string, refusals ...ConfigRefusal) DescCase {
	c := DescCase{Name: "config refused", Require: []string{path}}
	if len(refusals) > 0 {
		c.Require = append(c.Require, configHeader(refusals))
	}
	var own []string // the refused keys whose default is below their minimum (b.n4q)
	for _, r := range refusals {
		k := r.Key
		var msg string
		switch {
		case r.Retention:
			msg = fmt.Sprintf("[defaults] expire_retention_days = %d, outside its range 1 to %d days",
				r.Value, config.MaxExpireRetentionDays)
		case r.RelayTimeout:
			msg = fmt.Sprintf("[relay] timeout_seconds = %d, outside its range 1 to %d seconds",
				r.Value, config.MaxRelayTimeoutSeconds)
		case r.PauseTimeout:
			msg = fmt.Sprintf("[pause] timeout_seconds = %d, outside its range 1 to %d seconds",
				r.Value, config.MaxPauseTimeoutSeconds)
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
		if r.table() == "[tmux]" && k.DefaultValue() < r.Minimum {
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
