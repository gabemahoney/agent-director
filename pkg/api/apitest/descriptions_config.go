package apitest

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
)

// descriptions_config.go holds the shared description helper's case for a
// config file refused for its [tmux] values (SR-4.1), its [defaults]
// expire_retention_days (b.sgw), its [relay] or [pause] timeout_seconds
// (b.8q2) or its [pre_trust] lock_wait_seconds (b.kr4): ErrConfigMalformed's
// description names the file and lists the refused tables, states each
// refused value, then that a missing key, or 0, gives the default, adding,
// beside a refused key whose derived minimum is above its default, that it
// gives that key's safe minimum when that is larger (b.9e1). No refused
// value's text contains "; ", the separator between them. A file setting one
// key under names that differ only in letter case has its own case,
// DescConfigCaseVariant (b.p8n).

// ConfigRefusal is one refused [tmux] value: Key and its configured Value;
// Minimum, the safe minimum it is below (0 for a key without one, refused as
// negative); and, for a derived minimum, the effective Create timeout and
// Pipe-close wait it was computed from. With Retention set it is instead the
// refused [defaults] expire_retention_days Value, with RelayTimeout or
// PauseTimeout set the refused [relay] or [pause] timeout_seconds Value
// (b.8q2), and with PreTrustLockWait set the refused [pre_trust]
// lock_wait_seconds Value (b.kr4); Key is then unused.
type ConfigRefusal struct {
	Key              config.TmuxKey
	Value            int64
	Minimum          int64
	Derived          bool
	Create, Pipe     int64
	Retention        bool
	RelayTimeout     bool
	PauseTimeout     bool
	PreTrustLockWait bool
}

// configTables are the tables a refusal description lists, in its order.
var configTables = []string{"[defaults]", "[relay]", "[pause]", "[pre_trust]", "[tmux]"}

// table is the table r's key is in.
func (r ConfigRefusal) table() string {
	switch {
	case r.Retention:
		return "[defaults]"
	case r.RelayTimeout:
		return "[relay]"
	case r.PauseTimeout:
		return "[pause]"
	case r.PreTrustLockWait:
		return "[pre_trust]"
	}
	return "[tmux]"
}

// nameList joins names as a refusal lists them, the refused tables in
// configHeader and a key's names in DescConfigCaseVariant: one name alone, two
// joined by " and ", more separated by ", " with " and " before the last
// ("[defaults], [relay] and [tmux]").
func nameList(names []string) string {
	if len(names) <= 2 {
		return strings.Join(names, " and ")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// configHeader is the description's "refused <tables> values: " for refusals,
// the tables listed by nameList.
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
	return "refused " + nameList(tables) + " values: "
}

// DescConfigRefused is ErrConfigMalformed's description for the config file
// at path. With no refusals (a value of the wrong type) only the path is
// required.
func DescConfigRefused(path string, refusals ...ConfigRefusal) DescCase {
	c := DescCase{Name: "config refused", Require: []string{path}}
	if len(refusals) > 0 {
		c.Require = append(c.Require, configHeader(refusals))
	}
	var raised []string // the refused keys whose safe minimum is above their default (b.9e1)
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
		case r.PreTrustLockWait:
			msg = fmt.Sprintf("[pre_trust] lock_wait_seconds = %d, outside its range 1 to %d seconds",
				r.Value, config.MaxPreTrustLockWaitSeconds)
		case r.Minimum == 0:
			msg = fmt.Sprintf("[tmux] %s = %d, which must be positive", k.Name(), r.Value)
		default:
			msg = fmt.Sprintf("[tmux] %s = %d, below its safe minimum %d %s", k.Name(), r.Value, r.Minimum, k.Unit())
		}
		if r.Derived {
			msg += fmt.Sprintf(" (computed from the effective %s %d and %s %d)",
				config.TmuxCreateTimeoutMs.Name(), r.Create, config.TmuxPipeCloseWaitMs.Name(), r.Pipe)
			if r.Minimum > k.DefaultValue() {
				raised = append(raised, "[tmux] "+k.Name())
			}
		}
		c.Require = append(c.Require, msg)
	}
	switch {
	case len(refusals) == 0:
	case len(raised) == 0:
		c.Require = append(c.Require, "A missing key, or 0, gives the default.")
		c.Forbid = append(c.Forbid, "its safe minimum when that is larger")
	default:
		c.Require = append(c.Require, "A missing key, or 0, gives the default, or for "+nameList(raised)+
			" its safe minimum when that is larger.")
	}
	return c
}

// DescConfigCaseVariant is ErrConfigMalformed's description for the config
// file at path setting one key under names that differ only in letter case
// (b.p8n): it names the file and the names as written, in file order, and
// says to set the key once, never that a missing key gives the default.
func DescConfigCaseVariant(path string, names ...string) DescCase {
	return DescCase{Name: "config case variant", Require: []string{path,
		"refused keys set more than once, under names that differ only in letter case: " + nameList(names) + ".",
		"Set each key once, removing all but one of the names listed for it."},
		MustNot: []string{"gives the default"}}
}
