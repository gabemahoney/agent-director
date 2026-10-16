// Package dbpathfix runs install.sh's [store] db_path reader (b.2io), which
// also reads [log] error_log_path and [launch] claude_path and codex_path, its
// [store] busy_timeout_ms reader (b.c7f) and its agent-rename migration
// (b.ssz) on their own, and holds the matrices of config.toml contents and
// HOME values that the drift guards run through them and through Go's own
// resolution: pkg/api (config.Load, then resolveStorePath; LoadTemplate for
// the migration) and internal/store (sentinelPath). Cases, LogCases and
// LaunchCases are the reader's; RenameCases are the migration's.
//
// The reader takes the base directory its relative paths join onto as an
// argument (a Base), so a case runs under the new directory (once the move is
// done) and under the old one (while it is due), and its pre-flight form also
// reads [log] error_log_path (LogCases, ResolveWithLog).
//
// This is a LEAF test-support package: it imports nothing from agent-director,
// so any test package can use it. The reader runs under bash, as install.sh
// does.
package dbpathfix

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Expect is what a Case's config gives.
type Expect int

const (
	// Same: the reader prints exactly the store path Go resolves.
	Same Expect = iota
	// ReaderRefuses: the reader refuses the file (status 1, nothing on stdout),
	// so install.sh stops before anything on disk changes.
	ReaderRefuses
	// GoRefuses: the reader resolves a path, but config.Load refuses the file,
	// so the binary's own ErrConfigMalformed stops the install at step 3 or 4.
	GoRefuses
	// BothRefuse (LaunchCases): the reader refuses the file as ReaderRefuses
	// does, and config.Load refuses it too (ErrConfigMalformed), so the
	// refusal is one the binary would make itself. ReaderRefuses in
	// LaunchCases is a file config.Load accepts: the reader is stricter.
	BothRefuse
)

// Form is how a Case's config file exists on disk.
type Form int

const (
	File   Form = iota // a regular file holding Config
	NoFile             // no config file at all
	Dir                // a directory where the config file should be
)

// Case is one config.toml in the matrix.
type Case struct {
	Name   string
	Config string
	Expect Expect
	Form   Form
}

// Homes are the HOME values every case runs under, some needing cleaning.
var Homes = []string{"/home/u", "/home/u/", "//home//u/./", "/home/x/../u", "/"}

// store is a [store] table setting db_path to v, written as given.
func store(v string) string { return "[store]\ndb_path = " + v + "\n" }

// Cases is the matrix, the ReaderRefuses cases grouped by what Go does with them.
var Cases = []Case{
	{Name: "missing-file", Form: NoFile},
	{Name: "empty-file"},
	{Name: "store-table-only", Config: "[store]\n"},
	{Name: "empty-basic", Config: store(`""`)},
	{Name: "empty-literal", Config: store(`''`)},
	{Name: "abs", Config: store(`"/abs/x.db"`)},
	{Name: "abs-literal", Config: store(`'/abs/x.db'`)},
	{Name: "abs-unclean", Config: store(`"/abs//y/./z/../w/"`)},
	{Name: "abs-root", Config: store(`"/"`)},
	{Name: "abs-double-slash", Config: store(`"//x.db"`)},
	{Name: "abs-space", Config: store(`"/abs with space/s.db"`)},
	{Name: "tilde-slash", Config: store(`"~/x.db"`)},
	{Name: "tilde-alone", Config: store(`"~"`)},
	{Name: "tilde-slash-only", Config: store(`"~/"`)},
	{Name: "tilde-unclean", Config: store(`"~//x/./y/../z/"`)},
	{Name: "tilde-up", Config: store(`"~/../../.."`)},
	{Name: "tilde-user", Config: store(`"~user/x"`)},
	{Name: "rel", Config: store(`"x"`)},
	{Name: "rel-dot", Config: store(`"./x"`)},
	{Name: "rel-dotdot", Config: store(`"../x"`)},
	{Name: "rel-unclean", Config: store(`"a//b/./c/"`)},
	{Name: "rel-up-past-root", Config: store(`"../../../../.."`)},
	{Name: "dot", Config: store(`"."`)},
	{Name: "dotdot", Config: store(`".."`)},
	{Name: "rel-space", Config: store(`"with space/s.db"`)},
	{Name: "var", Config: store(`"$HOME/x"`)},
	{Name: "var-braced", Config: store(`"${HOME}/x"`)},
	{Name: "utf8", Config: store(`"é/x"`)},
	{Name: "hash-in-value", Config: store(`"#notcomment"`)},
	{Name: "literal-backslash", Config: store(`'C:\x'`)},
	{Name: "single-quote-in-basic", Config: store(`"it's"`)},
	{Name: "dquote-in-literal", Config: store(`'a"b'`)},
	{Name: "trailing-comment", Config: store(`"x" # comment "q" \ '`)},
	{Name: "literal-trailing-comment", Config: store(`'x' # 'q'`)},
	{Name: "spacing", Config: "[store]\n   db_path   =   \"x\"   #c\n"},
	{Name: "tight", Config: "[store]\ndb_path=\"x\"#c\n"},
	{Name: "tabs", Config: "[store]\n\tdb_path\t=\t\"x\"\t\n"},
	{Name: "header-spaces", Config: "[ store ]\ndb_path = \"/h\"\n"},
	{Name: "header-tabs", Config: "\t[\tstore\t]\t\ndb_path = \"/h\"\n"},
	{Name: "header-comment", Config: "[store] # here ]\ndb_path = \"/h\"\n"},
	{Name: "crlf", Config: "[store]\r\ndb_path = \"/crlf\"\r\n"},
	{Name: "bom", Config: "\xef\xbb\xbf[store]\ndb_path = \"/bom\"\n"},
	{Name: "bom-crlf", Config: "\xef\xbb\xbf[store]\r\ndb_path = '/bom'\r\n"},
	{Name: "no-final-newline", Config: "[store]\ndb_path = \"/nonl\""},
	{Name: "root-db-path", Config: "db_path = \"/nope\"\n"},
	{Name: "other-table", Config: "[log]\ndb_path = \"/nope\"\n"},
	{Name: "other-table-after-store", Config: "[store]\n[defaults]\ndb_path = \"/nope\"\n"},
	{Name: "store-key-in-other-table", Config: "[defaults]\nstore = { db_path = \"/nope\" }\n"},
	{Name: "go-field-name", Config: "[store]\nDbPath = \"/nope\"\n"},
	{Name: "hyphen-key", Config: "[store]\ndb-path = \"/nope\"\n"},
	{Name: "commented-out", Config: "[store]\n# db_path = \"/nope\"\n"},
	{Name: "other-keys", Config: "foo = 1\n[tmux]\nquery_timeout_ms = 500\n[store]\nother = 1\ndb_path = \"x\" \nmore = [1, 2]\n"},
	{Name: "full", Config: "# agent-director\n[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n\n[relay]\npoll_base_ms = 100\n\n" +
		"[store]\ndb_path = \"~/custom/state.db\" # moved\n\n[log]\nerror_log_path = \"~/x.log\"\n"},
	{Name: "inline-table-in-store-ignored", Config: "[store]\nt = { db_path = \"/nope\" }\n"},
	{Name: "root-inline-other", Config: "defaults = { relay_mode = \"off\" }\n"},

	// The reader refuses all the rest. Go moves the store for these:
	{Name: "root-dotted", Config: "store.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-spaced", Config: "store . db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-case", Config: "Store.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-quoted", Config: "\"store\".db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-literal", Config: "'store'.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-quoted-key", Config: "store.\"db_path\" = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-long-s", Config: "\"ſtore\".db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-inline", Config: "store = { db_path = \"/x\" }\n", Expect: ReaderRefuses},
	{Name: "root-inline-case", Config: "Store = { db_path = \"/x\" }\n", Expect: ReaderRefuses},
	{Name: "root-inline-quoted", Config: "\"store\" = { db_path = \"/x\" }\n", Expect: ReaderRefuses},
	{Name: "quoted-key", Config: "[store]\n\"db_path\" = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-key-literal", Config: "[store]\n'db_path' = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-key-case", Config: "[store]\n\"DB_PATH\" = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-key-escape", Config: "[store]\n\"db\\u005fpath\" = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header", Config: "[\"store\"]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-literal", Config: "['store']\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-case", Config: "[ \"STORE\" ]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-long-s", Config: "[\"ſtore\"]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-escape", Config: "[\"st\\u006fre\"]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-upper", Config: "[STORE]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-mixed", Config: "[Store]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "key-upper", Config: "[store]\nDB_PATH = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "key-mixed", Config: "[store]\nDb_Path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "escape-backslash", Config: store(`"a\\b"`), Expect: ReaderRefuses},
	{Name: "escape-quote", Config: store(`"a\"b"`), Expect: ReaderRefuses},
	{Name: "escape-unicode", Config: store(`"/\u0041"`), Expect: ReaderRefuses},
	{Name: "multiline-basic", Config: store(`"""/x"""`), Expect: ReaderRefuses},
	{Name: "multiline-literal", Config: store(`'''/x'''`), Expect: ReaderRefuses},
	{Name: "array-hides-lines", Config: "[store]\nx = [\n[1]\n]\ndb_path = \"/real\"\n", Expect: ReaderRefuses},
	{Name: "utf16-bom", Config: "\xff\xfe[store]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "utf16-bom-be", Config: "\xfe\xff[store]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "tab-in-value", Config: store("\"a\tb\""), Expect: ReaderRefuses},
	{Name: "question-mark", Config: store(`"/a/b?x.db"`), Expect: ReaderRefuses},
	// ... leaves it at the default for these:
	{Name: "multiline-hides-lines", Config: "[defaults]\nnote = \"\"\"\n[store]\ndb_path = \"/fake\"\n\"\"\"\n", Expect: ReaderRefuses},
	{Name: "array-continuation", Config: "[defaults]\nx = [\n 1,\n]\n", Expect: ReaderRefuses},
	{Name: "header-dotted", Config: "[store.x]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-dotted-after-store", Config: "[store]\n[store.x]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-other-dotted", Config: "[a.b]\nx = 1\n", Expect: ReaderRefuses},
	{Name: "header-aot-other", Config: "[store]\n[[hooks]]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-other", Config: "[\"defaults\"]\nrelay_mode = \"off\"\n", Expect: ReaderRefuses},
	{Name: "quoted-header-unknown", Config: "[store]\n[\"a b\"]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "key-dotted-other", Config: "[defaults]\na.b = 1\n", Expect: ReaderRefuses},
	{Name: "key-dotted-in-store", Config: "[store]\nx.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "key-dotted-store-in-other-table", Config: "[defaults]\nstore.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-nested", Config: "store.x.db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-other", Config: "defaults.relay_mode = \"off\"\n", Expect: ReaderRefuses},
	{Name: "root-dotted-unknown-quoted", Config: "\"a b\".db_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "root-key-holding-dot", Config: "\"store.db_path\" = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "quoted-key-other", Config: "[defaults]\n\"relay_mode\" = \"off\"\n", Expect: ReaderRefuses},
	{Name: "quoted-key-unknown", Config: "[store]\n\"db path\" = \"/x\"\n", Expect: ReaderRefuses},
	// ... and refuses these itself:
	{Name: "directory", Form: Dir, Expect: ReaderRefuses},
	{Name: "header-array-of-tables", Config: "[[store]]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-array-of-tables-quoted", Config: "[[\"Store\"]]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "header-unclosed", Config: "[defaults\n[store]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "key-dotted-db-path", Config: "[store]\ndb_path.x = 1\n", Expect: ReaderRefuses},
	{Name: "inline-table-in-store", Config: "[store]\nt = { db_path = \"/x\" }\ndb_path.y = 2\n", Expect: ReaderRefuses},
	{Name: "store-twice", Config: "[store]\n[store]\n", Expect: ReaderRefuses},
	{Name: "db-path-twice", Config: store(`"/x"`) + "db_path = \"/y\"\n", Expect: ReaderRefuses},
	{Name: "integer", Config: store(`5`), Expect: ReaderRefuses},
	{Name: "boolean", Config: store(`true`), Expect: ReaderRefuses},
	{Name: "array", Config: store(`["/x"]`), Expect: ReaderRefuses},
	{Name: "bare-value", Config: store(`~/x.db`), Expect: ReaderRefuses},
	{Name: "no-value", Config: "[store]\ndb_path =\n", Expect: ReaderRefuses},
	{Name: "junk-after-value", Config: store(`"x" junk`), Expect: ReaderRefuses},
	{Name: "two-strings", Config: store(`"x" "y"`), Expect: ReaderRefuses},
	{Name: "unterminated", Config: store(`"x`), Expect: ReaderRefuses},
	{Name: "header-junk", Config: "[store] x\ndb_path = \"/x\"\n", Expect: ReaderRefuses},
	{Name: "keyval-no-key", Config: "[store]\n= \"/x\"\n", Expect: ReaderRefuses},
	{Name: "bom-not-first-line", Config: "\n\xef\xbb\xbf[store]\ndb_path = \"/x\"\n", Expect: ReaderRefuses},

	// The reader passes these over; the binary refuses them itself.
	{Name: "bad-value-other-table", Config: "[defaults]\nrelay_mode = off\n", Expect: GoRefuses},
	{Name: "unterminated-other-table", Config: "[log]\nerror_log_path = \"x\n", Expect: GoRefuses},
	{Name: "refused-value", Config: "[defaults]\nexpire_retention_days = -1\n", Expect: GoRefuses},
	{Name: "invalid-utf8-in-value", Config: store("\"/a\xe9\""), Expect: GoRefuses},
	{Name: "control-char-in-comment", Config: "# \x01\n" + store(`"/x"`), Expect: GoRefuses},
	// One key of another table under two letter cases (b.p8n).
	{Name: "other-table-key-two-cases", Config: "[Defaults]\nrelay_mode = \"on\"\n\n[defaults]\nrelay_mode = \"off\"\n", Expect: GoRefuses},
}

// logFile is a [log] table setting error_log_path to v, written as given.
func logFile(v string) string { return "[log]\nerror_log_path = " + v + "\n" }

// LogCases are the configs of the [log] error_log_path half of the matrix, run
// through the reader's pre-flight form (ResolveWithLog) and through Go's own
// resolution. The reader reads error_log_path under db_path's rules, except
// that a '?' is no refusal in it. Same: the reader accepts the file and prints
// the error-log path Go resolves (an empty line when the file does not set it,
// or sets it empty, where Go resolves its default or no log); ReaderRefuses:
// it refuses the file with the pre-flight wording.
var LogCases = []Case{
	{Name: "log-missing-file", Form: NoFile},
	{Name: "log-empty-file"},
	{Name: "log-table-only", Config: "[log]\n"},
	{Name: "log-empty-basic", Config: logFile(`""`)},
	{Name: "log-empty-literal", Config: logFile(`''`)},
	{Name: "log-abs", Config: logFile(`"/abs/e.log"`)},
	{Name: "log-abs-literal", Config: logFile(`'/abs/e.log'`)},
	{Name: "log-abs-unclean", Config: logFile(`"/abs//y/./z/../w/"`)},
	{Name: "log-abs-root", Config: logFile(`"/"`)},
	{Name: "log-abs-double-slash", Config: logFile(`"//e.log"`)},
	{Name: "log-abs-space", Config: logFile(`"/abs with space/e.log"`)},
	{Name: "log-tilde-slash", Config: logFile(`"~/e.log"`)},
	{Name: "log-tilde-alone", Config: logFile(`"~"`)},
	{Name: "log-tilde-slash-only", Config: logFile(`"~/"`)},
	{Name: "log-tilde-unclean", Config: logFile(`"~//x/./y/../z/"`)},
	{Name: "log-tilde-up", Config: logFile(`"~/../../.."`)},
	{Name: "log-tilde-user", Config: logFile(`"~user/e.log"`)},
	{Name: "log-rel", Config: logFile(`"e.log"`)},
	{Name: "log-rel-dot", Config: logFile(`"./e.log"`)},
	{Name: "log-rel-dotdot", Config: logFile(`"../e.log"`)},
	{Name: "log-rel-unclean", Config: logFile(`"a//b/./c/"`)},
	{Name: "log-rel-up-past-root", Config: logFile(`"../../../../.."`)},
	{Name: "log-dot", Config: logFile(`"."`)},
	{Name: "log-dotdot", Config: logFile(`".."`)},
	{Name: "log-rel-space", Config: logFile(`"with space/e.log"`)},
	{Name: "log-var", Config: logFile(`"$HOME/e.log"`)},
	{Name: "log-utf8", Config: logFile(`"é/e.log"`)},
	{Name: "log-hash-in-value", Config: logFile(`"#notcomment"`)},
	{Name: "log-literal-backslash", Config: logFile(`'C:\e'`)},
	{Name: "log-trailing-comment", Config: logFile(`"e.log" # comment "q" \ '`)},
	{Name: "log-spacing", Config: "[log]\n   error_log_path   =   \"e.log\"   #c\n"},
	{Name: "log-tight", Config: "[log]\nerror_log_path=\"e.log\"#c\n"},
	{Name: "log-header-spaces", Config: "[ log ]\nerror_log_path = \"/h.log\"\n"},
	{Name: "log-header-comment", Config: "[log] # here ]\nerror_log_path = \"/h.log\"\n"},
	{Name: "log-crlf", Config: "[log]\r\nerror_log_path = \"/crlf.log\"\r\n"},
	{Name: "log-bom", Config: "\xef\xbb\xbf[log]\nerror_log_path = \"/bom.log\"\n"},
	{Name: "log-no-final-newline", Config: "[log]\nerror_log_path = \"/nonl.log\""},
	// A '?' is refused in db_path (it starts SQLite options) and accepted here:
	// the error log is opened as a plain file.
	{Name: "log-question-mark", Config: logFile(`"/a/b?x.log"`)},
	{Name: "log-question-mark-rel", Config: logFile(`"a?b.log"`)},
	// Both keys set, in either order, in any mix of forms.
	{Name: "log-with-store", Config: "[store]\ndb_path = \"s.db\"\n\n[log]\nerror_log_path = \"e.log\"\n"},
	{Name: "log-before-store", Config: "[log]\nerror_log_path = \"~/e.log\"\n\n[store]\ndb_path = \"/abs/s.db\"\n"},
	{Name: "log-store-only", Config: "[store]\ndb_path = \"s.db\"\n"},
	{Name: "log-other-keys", Config: "[log]\nother = 1\nerror_log_path = \"e.log\"\nmore = [1, 2]\n"},
	// Not the log's key: ignored, as Go ignores it.
	{Name: "log-root-key", Config: "error_log_path = \"/nope.log\"\n"},
	{Name: "log-key-in-store", Config: "[store]\nerror_log_path = \"/nope.log\"\n"},
	{Name: "log-key-in-defaults", Config: "[defaults]\nerror_log_path = \"/nope.log\"\n"},
	{Name: "log-db-path-in-log", Config: "[log]\ndb_path = \"/nope.db\"\n"},
	{Name: "log-go-field-name", Config: "[log]\nErrorLogPath = \"/nope.log\"\n"},
	{Name: "log-hyphen-key", Config: "[log]\nerror-log-path = \"/nope.log\"\n"},
	{Name: "log-commented-out", Config: "[log]\n# error_log_path = \"/nope.log\"\n"},
	{Name: "log-other-table-after-log", Config: "[log]\n[defaults]\nerror_log_path = \"/nope.log\"\n"},
	{Name: "log-inline-table-in-log-ignored", Config: "[log]\nt = { error_log_path = \"/nope.log\" }\n"},

	// The reader refuses all the rest, with the pre-flight wording.
	{Name: "log-root-dotted", Config: "log.error_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-root-dotted-case", Config: "Log.error_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-root-dotted-quoted", Config: "\"log\".error_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-root-inline", Config: "log = { error_log_path = \"/x.log\" }\n", Expect: ReaderRefuses},
	{Name: "log-root-inline-case", Config: "LOG = { error_log_path = \"/x.log\" }\n", Expect: ReaderRefuses},
	{Name: "log-quoted-key", Config: "[log]\n\"error_log_path\" = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-quoted-key-literal", Config: "[log]\n'error_log_path' = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-quoted-header", Config: "[\"log\"]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-quoted-header-case", Config: "[ \"LOG\" ]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-header-upper", Config: "[LOG]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-header-mixed", Config: "[Log]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-key-upper", Config: "[log]\nERROR_LOG_PATH = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-key-mixed", Config: "[log]\nError_Log_Path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-escape-backslash", Config: logFile(`"a\\b"`), Expect: ReaderRefuses},
	{Name: "log-escape-unicode", Config: logFile(`"/\u0041"`), Expect: ReaderRefuses},
	{Name: "log-multiline-basic", Config: logFile(`"""/x.log"""`), Expect: ReaderRefuses},
	{Name: "log-multiline-literal", Config: logFile(`'''/x.log'''`), Expect: ReaderRefuses},
	{Name: "log-tab-in-value", Config: logFile("\"a\tb\""), Expect: ReaderRefuses},
	{Name: "log-array-hides-lines", Config: "[log]\nx = [\n[1]\n]\nerror_log_path = \"/real.log\"\n", Expect: ReaderRefuses},
	{Name: "log-header-dotted", Config: "[log.x]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-key-dotted-in-log", Config: "[log]\nx.error_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-key-dotted-key", Config: "[log]\nerror_log_path.x = 1\n", Expect: ReaderRefuses},
	{Name: "log-header-array-of-tables", Config: "[[log]]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	{Name: "log-directory", Form: Dir, Expect: ReaderRefuses},
	{Name: "log-table-twice", Config: "[log]\n[log]\n", Expect: ReaderRefuses},
	{Name: "log-key-twice", Config: logFile(`"/x.log"`) + "error_log_path = \"/y.log\"\n", Expect: ReaderRefuses},
	{Name: "log-integer", Config: logFile(`5`), Expect: ReaderRefuses},
	{Name: "log-boolean", Config: logFile(`true`), Expect: ReaderRefuses},
	{Name: "log-array", Config: logFile(`["/x.log"]`), Expect: ReaderRefuses},
	{Name: "log-bare-value", Config: logFile(`~/x.log`), Expect: ReaderRefuses},
	{Name: "log-no-value", Config: "[log]\nerror_log_path =\n", Expect: ReaderRefuses},
	{Name: "log-junk-after-value", Config: logFile(`"x" junk`), Expect: ReaderRefuses},
	{Name: "log-unterminated", Config: logFile(`"x`), Expect: ReaderRefuses},
	{Name: "log-utf16-bom", Config: "\xff\xfe[log]\nerror_log_path = \"/x.log\"\n", Expect: ReaderRefuses},
	// A refusal in the other key still stops the read, with the log wording.
	{Name: "log-store-question-mark", Config: "[log]\nerror_log_path = \"/e.log\"\n[store]\ndb_path = \"/a?b.db\"\n", Expect: ReaderRefuses},
	{Name: "log-store-key-twice", Config: "[store]\ndb_path = \"/a\"\ndb_path = \"/b\"\n[log]\nerror_log_path = \"/e.log\"\n", Expect: ReaderRefuses},
}

// WriteConfig puts case number i of the matrix in dir and returns its path.
func WriteConfig(t *testing.T, dir string, i int, c Case) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%02d-%s.toml", i, c.Name))
	var err error
	switch c.Form {
	case File:
		err = os.WriteFile(path, []byte(c.Config), 0o600)
	case Dir:
		err = os.Mkdir(path, 0o700)
	}
	if err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	return path
}

// Reader is install.sh's reader block, extracted on its own.
type Reader struct{ script string }

// Result is one run of a reader function.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// NewReader extracts the block between install.sh's "# >>> ad_store_db_path"
// and "# <<< ad_store_db_path" lines into a temp file, as a test of it would
// with sed. Its Reader runs Resolve and Sentinel.
func NewReader(t *testing.T, installSh string) *Reader {
	t.Helper()
	return newBlockReader(t, installSh, "ad_store_db_path", "b.2io")
}

// NewUninstallReader extracts the same block from uninstall.sh, which carries a
// verbatim copy of it (b.ssz) for its [launch] claude_path read. Its Reader
// runs the same functions as NewReader's; a drift guard runs both.
func NewUninstallReader(t *testing.T, uninstallSh string) *Reader {
	t.Helper()
	return newBlockReader(t, uninstallSh, "ad_store_db_path", "b.2io")
}

// NewBusyTimeoutReader extracts install.sh's [store] busy_timeout_ms reader,
// the "# >>> ad_store_busy_timeout_ms" block (b.c7f), as NewReader does. Its
// Reader runs BusyTimeout.
func NewBusyTimeoutReader(t *testing.T, installSh string) *Reader {
	t.Helper()
	return newBlockReader(t, installSh, "ad_store_busy_timeout_ms", "b.c7f")
}

// newBlockReader extracts the block between install.sh's "# >>> <name>" and
// "# <<< <name>" lines (bug: the ticket that added it) into a temp file.
func newBlockReader(t *testing.T, installSh, name, bug string) *Reader {
	t.Helper()
	data, err := os.ReadFile(installSh)
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	open, end := "# >>> "+name+" ", "# <<< "+name+" "
	var block []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, open) {
			in = true
		}
		if in {
			block = append(block, line)
		}
		if in && strings.HasPrefix(line, end) {
			break
		}
	}
	if len(block) == 0 || !strings.HasPrefix(block[len(block)-1], end) {
		t.Fatalf("%s has no complete %q ... %q block (%s)", installSh, open, end, bug)
	}
	script := filepath.Join(t.TempDir(), "reader.sh")
	if err := os.WriteFile(script, []byte(strings.Join(block, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write reader: %v", err)
	}
	return &Reader{script: script}
}

// run calls the reader function fn with args under bash -u -o pipefail, as
// install.sh's set -euo pipefail runs it in an if condition.
func (r *Reader) run(t *testing.T, fn string, args ...string) Result {
	t.Helper()
	argv := append([]string{"-uo", "pipefail", "-c", `source "$1" && "$2" "${@:3}"`, "bash", r.script, fn}, args...)
	cmd := exec.Command("bash", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.Code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("bash %s: %v", fn, err)
	}
	return res
}

// Base is a base-directory column of the matrix: the directory the reader
// joins a config's relative paths onto, as a function of HOME. install.sh
// passes the old directory while a move is due (the config file is still
// there) and the new one otherwise. The package names only the new one, so
// the old one comes from the _test.go file that runs the matrix under it.
type Base struct {
	Name string
	Dir  func(home string) string
}

// NewBase is AD's directory under home, written as install.sh joins it (home,
// a slash, then the directory), so a home that needs cleaning still does. Go's
// resolution joins onto the same directory.
var NewBase = Base{Name: "new", Dir: func(home string) string { return home + "/.config/agent-director" }}

// Resolve runs ad_store_db_path <config> <home> <base> with NewBase's
// directory under home: the base the reader gets once the move is done.
func (r *Reader) Resolve(t *testing.T, config, home string) Result {
	t.Helper()
	return r.ResolveIn(t, config, home, NewBase)
}

// ResolveIn runs ad_store_db_path <config> <home> <base> with b's directory
// under home.
func (r *Reader) ResolveIn(t *testing.T, config, home string, b Base) Result {
	t.Helper()
	return r.run(t, "ad_store_db_path", config, home, b.Dir(home))
}

// ResolveWithLog runs ad_store_db_path <config> <home> <base> log: the
// pre-flight form, which also reads [log] error_log_path. On success Stdout
// holds two lines, the store path and the error-log path (an empty line when
// the file does not set it or sets it empty); Split reads them.
func (r *Reader) ResolveWithLog(t *testing.T, config, home string, b Base) Result {
	t.Helper()
	return r.run(t, "ad_store_db_path", config, home, b.Dir(home), "log")
}

// Split returns the store path and the error-log path of a successful
// ResolveWithLog, failing the test when Stdout is not exactly those two lines.
func (res Result) Split(t *testing.T) (db, errLog string) {
	t.Helper()
	db, rest, ok := strings.Cut(res.Stdout, "\n")
	errLog, tail, ok2 := strings.Cut(rest, "\n")
	if !ok || !ok2 || tail != "" {
		t.Fatalf("stdout %q is not two newline-ended lines", res.Stdout)
	}
	return db, errLog
}

// ResolveLaunch runs ad_store_db_path <config> <home> <base> launch: the form
// uninstall.sh and install.sh's non-move pre-flight use. On success Stdout
// holds four lines: store, claude_path, codex_path and cursor_path (an
// empty line for a key that is unset or empty); Lines(t, 4) reads them.
func (r *Reader) ResolveLaunch(t *testing.T, config, home string, b Base) Result {
	t.Helper()
	return r.run(t, "ad_store_db_path", config, home, b.Dir(home), "launch")
}

// ResolveLogLaunch runs ad_store_db_path <config> <home> <base> log launch:
// the pre-flight form while the move is due. On success Stdout holds five
// lines: store, error-log, claude_path, codex_path and cursor_path;
// Lines(t, 5) reads them.
func (r *Reader) ResolveLogLaunch(t *testing.T, config, home string, b Base) Result {
	t.Helper()
	return r.run(t, "ad_store_db_path", config, home, b.Dir(home), "log", "launch")
}

// Lines returns the n lines of a successful result, failing the test when
// Stdout is not exactly n newline-ended lines (an empty line counts).
func (res Result) Lines(t *testing.T, n int) []string {
	t.Helper()
	lines := strings.Split(res.Stdout, "\n")
	if len(lines) != n+1 || lines[n] != "" {
		t.Fatalf("stdout %q is not %d newline-ended lines", res.Stdout, n)
	}
	return lines[:n]
}

// BusyTimeout runs ad_store_busy_timeout_ms <config>.
func (r *Reader) BusyTimeout(t *testing.T, config string) Result {
	t.Helper()
	return r.run(t, "ad_store_busy_timeout_ms", config)
}

// Sentinel runs ad_sentinel_path <db> and returns the path it prints.
func (r *Reader) Sentinel(t *testing.T, db string) string {
	t.Helper()
	res := r.run(t, "ad_sentinel_path", db)
	if res.Code != 0 || res.Stderr != "" {
		t.Fatalf("ad_sentinel_path %q: status %d, stderr %q", db, res.Code, res.Stderr)
	}
	return strings.TrimSuffix(res.Stdout, "\n")
}

// NewMigrationReader extracts install.sh's agent-rename migration, the
// "# >>> ad_agent_rename_migrate" block (b.ssz), as NewReader does. Its Reader
// runs Migrate and MigrationErrName.
func NewMigrationReader(t *testing.T, installSh string) *Reader {
	t.Helper()
	return newBlockReader(t, installSh, "ad_agent_rename_migrate", "b.ssz")
}

// Migrate runs ad_agent_rename_migrate <kind> <file>. Stdout holds the
// migrated bytes, exact, on status 0; the status is 3 for nothing to change
// (or no such file) and 2 for a refusal, whose message is on Stderr.
func (r *Reader) Migrate(t *testing.T, kind Kind, file string) Result {
	t.Helper()
	return r.run(t, "ad_agent_rename_migrate", string(kind), file)
}

// MigrationErrName runs ad_agent_rename_err_name <kind>: the err_name the
// caller reports when Migrate refuses a file of that kind.
func (r *Reader) MigrationErrName(t *testing.T, kind Kind) string {
	t.Helper()
	res := r.run(t, "ad_agent_rename_err_name", string(kind))
	if res.Code != 0 || res.Stderr != "" {
		t.Fatalf("ad_agent_rename_err_name %s: status %d, stderr %q", kind, res.Code, res.Stderr)
	}
	return strings.TrimSuffix(res.Stdout, "\n")
}
