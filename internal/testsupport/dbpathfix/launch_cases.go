package dbpathfix

import "strings"

// LaunchCases are the configs of the [launch] claude_path and codex_path half
// of the matrix (SR-11.1, SR-5.0), run through the reader's launch form
// (ResolveLaunch, ResolveLogLaunch) and through Go's own resolution
// (config.Load's Launch). Every per-key shape is written for both keys.
//
// Same: the reader accepts the file and prints the paths config.Load resolves
// (an empty line for a key that is unset or empty), with either base: a
// launch path is never joined onto the base directory. ReaderRefuses: the
// reader refuses a file config.Load accepts, so it is stricter, as for letter
// case. BothRefuse: the reader refuses a file config.Load refuses too.
// GoRefuses: the reader passes over a file the binary refuses itself.
var LaunchCases = append(append(launchKeyCases("claude", "claude_path"), launchKeyCases("codex", "codex_path")...), []Case{
	{Name: "launch-missing-file", Form: NoFile},
	{Name: "launch-empty-file"},
	{Name: "launch-table-only", Config: "[launch]\n"},
	{Name: "launch-both-abs", Config: "[launch]\nclaude_path = \"/opt/c/claude\"\ncodex_path = \"/opt/x/codex\"\n"},
	{Name: "launch-both-mixed-forms", Config: "[launch]\nclaude_path = '~/bin/claude'\ncodex_path = \"/opt/x/codex\" # x\n"},
	{Name: "launch-both-one-empty", Config: "[launch]\nclaude_path = \"\"\ncodex_path = \"/opt/x/codex\"\n"},
	{Name: "launch-reversed-order", Config: "[launch]\ncodex_path = \"/opt/x/codex\"\nclaude_path = \"/opt/c/claude\"\n"},
	{Name: "launch-unknown-key", Config: "[launch]\nother = 1\nclaude_path = \"/c\"\nmore = [1, 2]\n"},
	{Name: "launch-with-store-and-log", Config: "[store]\ndb_path = \"s.db\"\n\n[launch]\nclaude_path = \"/c\"\n\n[log]\nerror_log_path = \"e.log\"\n"},
	{Name: "launch-before-store", Config: "[launch]\ncodex_path = \"~/x\"\n\n[store]\ndb_path = \"/abs/s.db\"\n"},
	// Not the launch table's keys: ignored, as Go ignores them.
	{Name: "launch-root-key", Config: "claude_path = \"/nope\"\n"},
	{Name: "launch-key-in-defaults", Config: "[defaults]\nclaude_path = \"/nope\"\ncodex_path = \"/nope\"\n"},
	{Name: "launch-key-in-store", Config: "[store]\nclaude_path = \"/nope\"\n"},
	{Name: "launch-key-after-launch", Config: "[launch]\n[defaults]\nclaude_path = \"/nope\"\n"},
	{Name: "launch-inline-table-ignored", Config: "[launch]\nt = { claude_path = \"/nope\" }\n"},
	{Name: "launch-commented-out", Config: "[launch]\n# claude_path = \"/nope\"\n"},
	{Name: "launch-go-field-name", Config: "[launch]\nClaudePath = \"/nope\"\n"},
	{Name: "launch-hyphen-key", Config: "[launch]\nclaude-path = \"/nope\"\n"},
	{Name: "launch-store-key-in-launch", Config: "[launch]\ndb_path = \"/nope\"\n"},
	// One launch table only.
	{Name: "launch-table-twice", Config: "[launch]\n[launch]\n", Expect: BothRefuse},
	{Name: "launch-table-twice-keys", Config: "[launch]\nclaude_path = \"/a\"\n[launch]\ncodex_path = \"/b\"\n", Expect: BothRefuse},
	{Name: "launch-header-array-of-tables", Config: "[[launch]]\nclaude_path = \"/x\"\n", Expect: BothRefuse},
	{Name: "launch-header-array-of-tables-case", Config: "[[Launch]]\nclaude_path = \"/x\"\n", Expect: BothRefuse},
	{Name: "launch-directory", Form: Dir, Expect: BothRefuse},
	// The reader passes these over; the binary refuses them itself.
	{Name: "launch-bad-value-other-table", Config: "[launch]\nclaude_path = \"/c\"\n[defaults]\nrelay_mode = off\n", Expect: GoRefuses},
	{Name: "launch-refused-value-other-table", Config: "[launch]\ncodex_path = \"/c\"\n[defaults]\nexpire_retention_days = -1\n", Expect: GoRefuses},
}...)

// launchKeyCases is the matrix of one [launch] key, written under its own
// prefix: key is claude_path or codex_path.
func launchKeyCases(prefix, key string) []Case {
	kv := func(v string) string { return "[launch]\n" + key + " = " + v + "\n" }
	upper := strings.ToUpper(key)
	mixed := strings.ToUpper(prefix[:1]) + prefix[1:] + "_Path"
	c := func(name, config string, expect Expect) Case {
		return Case{Name: prefix + "-" + name, Config: config, Expect: expect}
	}
	return []Case{
		// Accepted, with the resolved value.
		c("abs", kv(`"/opt/x/prog"`), Same),
		c("abs-literal", kv(`'/opt/x/prog'`), Same),
		c("abs-unclean", kv(`"/abs//y/./z/../w/"`), Same),
		c("abs-root", kv(`"/"`), Same),
		c("abs-space", kv(`"/abs with space/prog"`), Same),
		c("abs-utf8", kv(`"/é/prog"`), Same),
		c("abs-var-literal", kv(`"/$HOME/prog"`), Same),
		c("abs-hash", kv(`"/a#b"`), Same),
		c("abs-question-mark", kv(`"/a/b?x"`), Same),
		c("abs-literal-backslash", kv(`'/C:\x'`), Same),
		c("empty-basic", kv(`""`), Same),
		c("empty-literal", kv(`''`), Same),
		c("tilde-slash", kv(`"~/bin/prog"`), Same),
		c("tilde-slash-literal", kv(`'~/bin/prog'`), Same),
		c("tilde-slash-only", kv(`"~/"`), Same),
		c("tilde-unclean", kv(`"~//x/./y/../z/"`), Same),
		c("tilde-up", kv(`"~/../../.."`), Same),
		c("trailing-comment", kv(`"/x" # comment "q" \ '`), Same),
		c("literal-trailing-comment", kv(`'/x' # 'q'`), Same),
		c("spacing", "[launch]\n   "+key+"   =   \"/x\"   #c\n", Same),
		c("tight", "[launch]\n"+key+"=\"/x\"#c\n", Same),
		c("tabs", "[launch]\n\t"+key+"\t=\t\"/x\"\t\n", Same),
		c("header-spaces", "[ launch ]\n"+key+" = \"/h\"\n", Same),
		c("header-tabs", "\t[\tlaunch\t]\t\n"+key+" = \"/h\"\n", Same),
		c("header-comment", "[launch] # here ]\n"+key+" = \"/h\"\n", Same),
		c("crlf", "[launch]\r\n"+key+" = \"/crlf\"\r\n", Same),
		c("bom", "\xef\xbb\xbf[launch]\n"+key+" = \"/bom\"\n", Same),
		c("bom-crlf", "\xef\xbb\xbf[launch]\r\n"+key+" = '~/bom'\r\n", Same),
		c("no-final-newline", "[launch]\n"+key+" = \"/nonl\"", Same),

		// The reader refuses these. Go accepts them (letter case, forms the
		// reader does not read):
		c("header-upper", "[LAUNCH]\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("header-mixed", "[Launch]\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("key-upper", "[launch]\n"+upper+" = \"/x\"\n", ReaderRefuses),
		c("key-mixed", "[launch]\n"+mixed+" = \"/x\"\n", ReaderRefuses),
		c("quoted-header", "[\"launch\"]\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("quoted-header-literal", "['launch']\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("quoted-header-case", "[ \"LAUNCH\" ]\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("quoted-key", "[launch]\n\""+key+"\" = \"/x\"\n", ReaderRefuses),
		c("quoted-key-literal", "[launch]\n'"+key+"' = \"/x\"\n", ReaderRefuses),
		c("root-dotted", "launch."+key+" = \"/x\"\n", ReaderRefuses),
		c("root-dotted-spaced", "launch . "+key+" = \"/x\"\n", ReaderRefuses),
		c("root-dotted-case", "Launch."+key+" = \"/x\"\n", ReaderRefuses),
		c("root-dotted-quoted", "\"launch\"."+key+" = \"/x\"\n", ReaderRefuses),
		c("root-inline", "launch = { "+key+" = \"/x\" }\n", ReaderRefuses),
		c("root-inline-case", "LAUNCH = { "+key+" = \"/x\" }\n", ReaderRefuses),
		c("header-dotted", "[launch.x]\n"+key+" = \"/x\"\n", ReaderRefuses),
		c("key-dotted-in-launch", "[launch]\nx."+key+" = \"/x\"\n", ReaderRefuses),
		c("escape-backslash", kv(`"/a\\b"`), ReaderRefuses),
		c("escape-quote", kv(`"/a\"b"`), ReaderRefuses),
		c("escape-unicode", kv(`"/\u0041"`), ReaderRefuses),
		c("multiline-basic", kv(`"""/x"""`), ReaderRefuses),
		c("multiline-literal", kv(`'''/x'''`), ReaderRefuses),
		c("tab-in-value", kv("\"/a\tb\""), ReaderRefuses),
		c("array-hides-lines", "[launch]\nx = [\n[1]\n]\n"+key+" = \"/real\"\n", ReaderRefuses),
		c("utf16-bom", "\xff\xfe[launch]\n"+key+" = \"/x\"\n", ReaderRefuses),

		// Both refuse these. A launch value is empty, absolute or ~/...:
		c("rel", kv(`"prog"`), BothRefuse),
		c("rel-dot", kv(`"./prog"`), BothRefuse),
		c("rel-dotdot", kv(`"../prog"`), BothRefuse),
		c("rel-dir", kv(`"bin/prog"`), BothRefuse),
		c("dot", kv(`"."`), BothRefuse),
		c("blank", kv(`" "`), BothRefuse),
		c("blank-tab-literal", kv("'\t'"), BothRefuse),
		c("tilde-alone", kv(`"~"`), BothRefuse),
		c("tilde-user", kv(`"~user/prog"`), BothRefuse),
		c("var-relative", kv(`"$HOME/prog"`), BothRefuse),
		c("var-braced-relative", kv(`"${HOME}/prog"`), BothRefuse),
		// ... and a value in no accepted form, or a key set twice:
		c("key-twice", kv(`"/x"`)+key+" = \"/y\"\n", BothRefuse),
		c("key-twice-empty", kv(`""`)+key+" = \"\"\n", BothRefuse),
		c("integer", kv(`5`), BothRefuse),
		c("boolean", kv(`true`), BothRefuse),
		c("array", kv(`["/x"]`), BothRefuse),
		c("bare-value", kv(`~/prog`), BothRefuse),
		c("no-value", "[launch]\n"+key+" =\n", BothRefuse),
		c("junk-after-value", kv(`"/x" junk`), BothRefuse),
		c("two-strings", kv(`"/x" "/y"`), BothRefuse),
		c("unterminated", kv(`"/x`), BothRefuse),
		c("control-char", kv("\"/a\x01b\""), BothRefuse),
		c("key-dotted-key", "[launch]\n"+key+".x = 1\n", BothRefuse),
	}
}
