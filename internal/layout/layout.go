// Package layout is the one definition of where agent-director keeps its
// files: the directory .config/agent-director under the user's home, and the
// names of the files and directories in it (SR-11.7). Every default path in
// the Go code derives from it. It imports only the standard library, so
// internal/config, internal/trail, internal/store and the rest can all import
// it without a cycle.
//
// The home directory is always the one os.UserHomeDir gives ($HOME on Unix):
// never the passwd entry, and XDG_CONFIG_HOME is never read.
package layout

import "path/filepath"

// DirRel is AD's directory relative to the home directory, in slash form.
const DirRel = ".config/agent-director"

// Display is AD's directory as users and descriptions write it, with "~" for
// the home directory. It is also the literal both binaries' version result
// reports as config_dir.
const Display = "~/" + DirRel

// The names of the entries in AD's directory.
const (
	ConfigFile   = "config.toml"
	StoreFile    = "state.db"
	ErrorLogFile = "errors.log"
	TrailFile    = "ad-trail.jsonl"
	TemplatesDir = "templates"
	BinDir       = "bin"
	// BinaryName is the installed agent-director binary's file name.
	BinaryName = "agent-director"
)

// The defaults in their "~/" form, as config files and descriptions write
// them.
const (
	DisplayConfigPath   = Display + "/" + ConfigFile
	DisplayStorePath    = Display + "/" + StoreFile
	DisplayErrorLogPath = Display + "/" + ErrorLogFile
	DisplayTemplatesDir = Display + "/" + TemplatesDir
)

// Dir returns AD's directory under home. With an empty home it is the
// relative path DirRel, which callers refuse or skip as they always did when
// no home directory could be determined.
func Dir(home string) string {
	return filepath.Join(home, filepath.FromSlash(DirRel))
}

// Join returns the path of an entry under AD's directory under home.
func Join(home string, elem ...string) string {
	return filepath.Join(append([]string{Dir(home)}, elem...)...)
}
