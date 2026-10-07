// Package dbpathfix runs install.sh's [store] db_path reader (b.2io) and its
// [store] busy_timeout_ms reader (b.c7f) on their own, and holds the one
// matrix of config.toml contents and HOME values that the db_path drift guards
// run through it and through Go's own resolution: pkg/api (config.Load, then
// resolveStorePath) and internal/store (sentinelPath).
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

// Resolve runs ad_store_db_path <config> <home>.
func (r *Reader) Resolve(t *testing.T, config, home string) Result {
	t.Helper()
	return r.run(t, "ad_store_db_path", config, home)
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
