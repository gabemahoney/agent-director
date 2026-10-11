// Package oldnames is the one table of agent-director's old, Claude-named
// public names, and the refusal texts built from it (SR-4.3). Every surface
// that refuses an old name reads this table: the CLI of both binaries (an old
// flag), the MCP dispatcher (an old parameter key), the config loader (an old
// [defaults] key) and the template loader (an old template key). A name that
// is in no row of Table is not an old name here and keeps the surface's
// existing unknown-name handling.
//
// The table is the only place in non-test Go, besides internal/store's
// column-rename hop, where an old name is written. The SR-4.5 old-names test
// names this package as its one exception.
package oldnames

import (
	"errors"
	"flag"
	"strings"
)

// Surface says where an old name was accepted.
type Surface int

const (
	// CLI is a flag of agent-director or agent-director-admin, without its
	// leading dashes.
	CLI Surface = iota
	// MCP is a tool parameter key.
	MCP
	// Config is a key of config.toml's [defaults] table.
	Config
	// Template is a top-level key of a template file.
	Template
)

// Entry is one old name and the name that replaced it on one surface.
type Entry struct {
	Surface Surface
	// Old is the refused name, as the surface spelled it.
	Old string
	// New is the name to use instead, as the surface spells it.
	New string
}

// Table is every old name, by surface. Config and Template names are matched
// regardless of letter case (Find), as their decoder matches names.
var Table = []Entry{
	{Surface: CLI, Old: "claude-instance-id", New: "instance-id"},
	{Surface: CLI, Old: "claude-args", New: "agent-args"},
	{Surface: MCP, Old: "claude_instance_id", New: "instance_id"},
	{Surface: MCP, Old: "claude_args", New: "agent_args"},
	{Surface: Config, Old: "disable_askuserquestion", New: "disable_ask_user"},
	{Surface: Template, Old: "claude_args", New: "agent_args"},
}

// Find returns the entry of surface s whose old name is name. CLI and MCP
// names match exactly; Config and Template names match regardless of letter
// case, as the TOML decoder matches them.
func Find(s Surface, name string) (Entry, bool) {
	for _, e := range Table {
		if e.Surface != s {
			continue
		}
		if e.Old == name || ((s == Config || s == Template) && strings.EqualFold(e.Old, name)) {
			return e, true
		}
	}
	return Entry{}, false
}

// FlagText is the description of the CLI refusal of old flag e: it names the
// new flag.
func (e Entry) FlagText() string {
	return "--" + e.Old + " is no longer accepted; use --" + e.New
}

// KeyText is the description of the refusal of old key e on a surface that
// names a key by its bare name (an MCP parameter, a template key): it names
// the new key.
func (e Entry) KeyText() string {
	return e.Old + " is no longer accepted; use " + e.New
}

// ConfigText is the description of the refusal of the old [defaults] key e:
// it names the new key.
func (e Entry) ConfigText() string {
	return "[defaults] " + e.Old + " is no longer accepted; use " + e.New
}

// unknownFlagPrefix starts the error the flag package returns for a flag a
// FlagSet does not define, in every form it accepts (-x, --x, -x=v, --x v).
const unknownFlagPrefix = "flag provided but not defined: -"

// RefuseFlag reports whether err, the error of fs.Parse, says that the
// FlagSet fs was given an old flag whose new flag fs defines, and returns the
// description of the refusal naming the new flag. The flag package has parsed
// every form of the flag and stopped at it, so a value that merely looks like
// an old flag (--text --claude-instance-id) and everything after a "--" is
// never mistaken for one. A FlagSet that does not define the new flag (spawn
// has no --agent-args) keeps its unknown-flag error: RefuseFlag reports false.
func RefuseFlag(fs *flag.FlagSet, err error) (string, bool) {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return "", false
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, unknownFlagPrefix) {
		return "", false
	}
	e, ok := Find(CLI, strings.TrimPrefix(msg, unknownFlagPrefix))
	if !ok || fs.Lookup(e.New) == nil {
		return "", false
	}
	return e.FlagText(), true
}
