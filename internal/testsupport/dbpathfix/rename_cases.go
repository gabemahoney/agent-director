package dbpathfix

import "strings"

// Kind is the kind of file install.sh's agent-rename migration reads.
type Kind string

const (
	// KindConfig is config.toml: [defaults] disable_askuserquestion becomes
	// disable_ask_user, in place.
	KindConfig Kind = "config"
	// KindTemplate is a stored template: its top-level part's claude_args
	// becomes agent_args, and agent = "claude" is added when it names no agent.
	KindTemplate Kind = "template"
)

// Outcome is what the migration does with a RenameCase's file.
type Outcome int

const (
	// Migrated: status 0, the migrated bytes on stdout are exactly Out.
	Migrated Outcome = iota
	// Unchanged: status 3, nothing on stdout, the file is left as it is.
	Unchanged
	// Refused: status 2, nothing on stdout, a message on stderr whose first
	// line is Headline and whose last two lines are RefusalTail.
	Refused
)

// Want is what the loaders read from the file a case ends with: Out for a
// Migrated case, In for an Unchanged one. config.Load reads a config
// (AskUser); config.LoadTemplate reads a template (Agent, Args, Labels). The
// loader refuses the file instead when LoaderRefuses is set: the migration
// passes over a shape the loader refuses (a value it does not read, say), and
// the case says so rather than hiding it.
type Want struct {
	AskUser       bool
	Agent         string
	Args          []string
	Labels        map[string]string
	LoaderRefuses bool
}

// RenameCase is one config.toml or template file in the migration's matrix.
type RenameCase struct {
	Name string
	Kind Kind
	In   string
	// Missing: the file does not exist (In is ignored). Status 3.
	Missing bool
	Outcome Outcome
	// Out is the exact migrated file, for Migrated.
	Out string
	// Headline is the first stderr line of a refusal, with %s for the file's
	// path where the message names the file; Show are the "  line N  : text"
	// lines it must also hold.
	Headline string
	Show     []string
	Want     Want
	// InRefused: for Migrated, the loader refuses In itself (an old key, or
	// agent_args with no agent), so the migration is what makes the file load.
	InRefused bool
}

// RefusalTail is the last two stderr lines of every refusal.
var RefusalTail = []string{
	"  See \"`install.sh` refuses config.toml or a template\" in docs/operator-actions.md.",
	"  Nothing was installed or changed. Re-run this install after the change.",
}

const (
	headlineDuplicate  = "install.sh: config.toml sets the question-tool key more than once under [defaults]; refusing to install."
	headlineInline     = "install.sh: config.toml sets defaults as a key and names disable_askuserquestion in it; refusing to install."
	headlineUnreadable = "install.sh: template %s holds a line the migration cannot read; refusing to install."
	headlineClaudeTwo  = "install.sh: template %s sets claude_args more than once; refusing to install."
	headlineBoth       = "install.sh: template %s sets both claude_args and agent_args; refusing to install."

	utf8BOM   = "\xef\xbb\xbf"
	agentLine = "agent = \"claude\"\n"
	// labelsTail is a table the migration must leave alone, and the loader
	// reads: owner = "me" under [labels].
	labelsTail = "[labels]\nowner = \"me\"\n"
)

var ownerLabels = map[string]string{"owner": "me"}

// RenameCases is the matrix: config.toml cases, then template cases (a line
// shape in the top-level part and in a table, headers, file forms, refusals).
var RenameCases = renameCases()

func renameCases() []RenameCase {
	var out []RenameCase
	out = append(out, configRenameCases()...)
	out = append(out, templateShapeCases()...)
	out = append(out, templateTableCases()...)
	out = append(out, templateHeaderCases()...)
	out = append(out, templateFormCases()...)
	out = append(out, templateRuleCases()...)
	out = append(out, templateRefusalCases()...)
	return out
}

// ---- config.toml ----

func configRenameCases() []RenameCase {
	yes, no := Want{AskUser: true}, Want{}
	// mig: the old key is renamed in place and the file loads.
	mig := func(name, in, out string, want Want) RenameCase {
		return RenameCase{Name: "config-" + name, Kind: KindConfig, In: in, Outcome: Migrated, Out: out, Want: want, InRefused: true}
	}
	// same: the migration changes nothing.
	same := func(name, in string, want Want) RenameCase {
		return RenameCase{Name: "config-" + name, Kind: KindConfig, In: in, Outcome: Unchanged, Want: want}
	}
	// refuse: the migration refuses the file; show are the lines it names.
	refuse := func(name, in, headline string, show ...string) RenameCase {
		return RenameCase{Name: "config-" + name, Kind: KindConfig, In: in, Outcome: Refused, Headline: headline, Show: show}
	}
	const (
		old = "disable_askuserquestion"
		new = "disable_ask_user"
	)
	return []RenameCase{
		// The key is renamed, every other byte kept.
		mig("basic", "[defaults]\n"+old+" = true\n", "[defaults]\n"+new+" = true\n", yes),
		mig("false", "[defaults]\n"+old+" = false\n", "[defaults]\n"+new+" = false\n", no),
		mig("spaced", "[defaults]\n  "+old+"   =   true   # c\n", "[defaults]\n  "+new+"   =   true   # c\n", yes),
		mig("tight", "[defaults]\n"+old+"=true#c\n", "[defaults]\n"+new+"=true#c\n", yes),
		mig("tabs", "[defaults]\n\t"+old+"\t=\ttrue\t\n", "[defaults]\n\t"+new+"\t=\ttrue\t\n", yes),
		mig("key-upper", "[defaults]\nDISABLE_ASKUSERQUESTION = true\n", "[defaults]\n"+new+" = true\n", yes),
		mig("key-mixed", "[defaults]\nDisable_AskUserQuestion = true\n", "[defaults]\n"+new+" = true\n", yes),
		mig("header-upper", "[DEFAULTS]\n"+old+" = true\n", "[DEFAULTS]\n"+new+" = true\n", yes),
		mig("header-mixed", "[Defaults]\n"+old+" = true\n", "[Defaults]\n"+new+" = true\n", yes),
		mig("header-spaces", "[ defaults ]\n"+old+" = true\n", "[ defaults ]\n"+new+" = true\n", yes),
		mig("header-tabs", "\t[\tdefaults\t]\t\n"+old+" = true\n", "\t[\tdefaults\t]\t\n"+new+" = true\n", yes),
		mig("header-comment", "[defaults] # the [defaults]\n"+old+" = true\n", "[defaults] # the [defaults]\n"+new+" = true\n", yes),
		mig("among-keys", "[defaults]\nexpire_retention_days = 3\n"+old+" = true\ninject_help_hook = true\n",
			"[defaults]\nexpire_retention_days = 3\n"+new+" = true\ninject_help_hook = true\n", yes),
		mig("between-tables", "[store]\ndb_path = \"/s.db\"\n\n[defaults]\n"+old+" = true\n\n[log]\nerror_log_path = \"/e.log\"\n",
			"[store]\ndb_path = \"/s.db\"\n\n[defaults]\n"+new+" = true\n\n[log]\nerror_log_path = \"/e.log\"\n", yes),
		mig("comment-mentions-key", "# "+old+" = true\n[defaults]\n"+old+" = true # "+old+"\n",
			"# "+old+" = true\n[defaults]\n"+new+" = true # "+old+"\n", yes),
		mig("old-also-under-store", "[store]\n"+old+" = true\n\n[defaults]\n"+old+" = false\n",
			"[store]\n"+old+" = true\n\n[defaults]\n"+new+" = false\n", no),
		mig("two-defaults-headers-one-key", "[defaults]\nexpire_retention_days = 3\n\n[DEFAULTS]\n"+old+" = true\n",
			"[defaults]\nexpire_retention_days = 3\n\n[DEFAULTS]\n"+new+" = true\n", yes),
		mig("no-final-newline", "[defaults]\n"+old+" = true", "[defaults]\n"+new+" = true", yes),
		mig("crlf", "[defaults]\r\n"+old+" = true\r\n", "[defaults]\r\n"+new+" = true\r\n", yes),
		mig("bom", utf8BOM+"[defaults]\n"+old+" = true\n", utf8BOM+"[defaults]\n"+new+" = true\n", yes),
		mig("bom-crlf", utf8BOM+"[defaults]\r\n"+old+" = true\r\n", utf8BOM+"[defaults]\r\n"+new+" = true\r\n", yes),
		mig("crlf-no-final-newline", "[defaults]\r\n"+old+" = true", "[defaults]\r\n"+new+" = true", yes),

		// Already migrated, or nothing to rename: left byte for byte.
		{Name: "config-missing-file", Kind: KindConfig, Missing: true, Outcome: Unchanged},
		same("empty-file", "", no),
		same("blank-lines-only", "\n\n  \n", no),
		same("new-name", "[defaults]\n"+new+" = true\n", yes),
		same("new-name-upper", "[defaults]\nDISABLE_ASK_USER = true\n", yes),
		same("new-name-crlf", "[defaults]\r\n"+new+" = true\r\n", yes),
		same("no-defaults-table", "[store]\ndb_path = \"/s.db\"\n", no),
		same("defaults-without-the-key", "[defaults]\nexpire_retention_days = 3\n", no),
		same("old-in-comment-only", "[defaults]\n# "+old+" = true\n", no),
		same("old-key-other-table-store", "[store]\n"+old+" = true\n", no),
		same("old-key-other-table-launch", "[launch]\n"+old+" = true\n", no),
		same("old-key-other-table-log", "[log]\n"+old+" = true\n", no),
		same("old-key-at-root", old+" = true\n[defaults]\n", no),
		same("old-key-after-defaults", "[defaults]\n[store]\n"+old+" = true\n", no),
		same("old-key-in-subtable", "[defaults.x]\n"+old+" = true\n", no),
		same("old-key-longer-name", "[defaults]\n"+old+"_x = 1\nx_"+old+" = 1\n", no),
		same("inline-defaults-new-name", "defaults = { "+new+" = true }\n", yes),
		same("inline-defaults-other-key", "defaults = { expire_retention_days = 3 }\n", no),
		// The migration reads a header of one bare name; another form ends
		// the [defaults] table it was in. The loader refuses these (the old
		// key under defaults, however it is written); ad_store_db_path refuses
		// them first, so an install never reaches the migration with one.
		same("old-key-array-of-tables", "[[defaults]]\n"+old+" = true\n", Want{LoaderRefuses: true}),
		same("old-key-quoted-header", "[\"defaults\"]\n"+old+" = true\n", Want{LoaderRefuses: true}),
		same("old-key-dotted-at-root", "defaults."+old+" = true\n", Want{LoaderRefuses: true}),

		// One old or new key under [defaults], all headers together.
		refuse("old-twice", "[defaults]\n"+old+" = true\n"+old+" = false\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 3  : "+old+" = false"),
		refuse("old-twice-other-case", "[defaults]\n"+old+" = true\nDisable_AskUserQuestion = false\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 3  : Disable_AskUserQuestion = false"),
		refuse("old-then-new", "[defaults]\n"+old+" = true\n"+new+" = true\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 3  : "+new+" = true"),
		refuse("new-then-old", "[defaults]\n"+new+" = true\n"+old+" = true\n", headlineDuplicate,
			"  line 2  : "+new+" = true", "  line 3  : "+old+" = true"),
		refuse("new-twice", "[defaults]\n"+new+" = true\nDISABLE_ASK_USER = true\n", headlineDuplicate,
			"  line 2  : "+new+" = true", "  line 3  : DISABLE_ASK_USER = true"),
		refuse("one-under-each-of-two-headers", "[defaults]\n"+old+" = true\n\n[DEFAULTS]\n"+old+" = false\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 5  : "+old+" = false"),
		refuse("one-under-each-of-two-same-headers", "[defaults]\n"+old+" = true\n[defaults]\n"+new+" = false\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 4  : "+new+" = false"),
		refuse("old-twice-crlf", "[defaults]\r\n"+old+" = true\r\n"+old+" = false\r\n", headlineDuplicate,
			"  line 2  : "+old+" = true", "  line 3  : "+old+" = false"),
		// defaults set as a key, on a line naming the old key.
		refuse("inline-defaults", "defaults = { "+old+" = true }\n", headlineInline,
			"  line 1  : defaults = { "+old+" = true }"),
		refuse("inline-defaults-upper", "DEFAULTS = {DISABLE_ASKUSERQUESTION = true}\n", headlineInline,
			"  line 1  : DEFAULTS = {DISABLE_ASKUSERQUESTION = true}"),
		refuse("inline-defaults-after-comments", "# c\n\n  defaults={"+old+"=true,expire_retention_days=3}\n", headlineInline,
			"  line 3  :   defaults={"+old+"=true,expire_retention_days=3}"),
		refuse("inline-defaults-twice", "defaults = { "+old+" = true }\nDefaults = { "+old+" = false }\n", headlineInline,
			"  line 1  : defaults = { "+old+" = true }", "  line 2  : Defaults = { "+old+" = false }"),
		refuse("inline-defaults-and-duplicate", "defaults = { "+old+" = true }\n[store]\n[defaults]\n"+old+" = true\n"+old+" = false\n", headlineInline,
			"  line 1  : defaults = { "+old+" = true }", "  line 4  : "+old+" = true", "  line 5  : "+old+" = false"),
	}
}

// ---- templates: line shapes ----

// topShape is a line (or lines, each ending "\n") of a template's top-level
// part. out is the line after migration (line itself when "" and the line
// is not renamed); agent: the line names agent, so no agent line is added;
// want is what the loader reads from the migrated file (Labels aside).
type topShape struct {
	name      string
	line, out string
	agent     bool
	want      Want
	inRefused bool
}

func claudeWant(args ...string) Want { return Want{Agent: "claude", Args: args} }

var topShapes = []topShape{
	// claude_args is renamed in place, and agent = "claude" is added.
	{name: "claude-args", line: "claude_args = [\"--x\"]\n", out: "agent_args = [\"--x\"]\n", want: claudeWant("--x"), inRefused: true},
	{name: "claude-args-spaced", line: "  claude_args   =   [\"--x\", 'y']   # c\n", out: "  agent_args   =   [\"--x\", 'y']   # c\n", want: claudeWant("--x", "y"), inRefused: true},
	{name: "claude-args-tight", line: "claude_args=[\"--x\"]#c\n", out: "agent_args=[\"--x\"]#c\n", want: claudeWant("--x"), inRefused: true},
	{name: "claude-args-tabs", line: "\tclaude_args\t=\t[\"--x\"]\t\n", out: "\tagent_args\t=\t[\"--x\"]\t\n", want: claudeWant("--x"), inRefused: true},
	{name: "claude-args-upper", line: "CLAUDE_ARGS = [\"--x\"]\n", out: "agent_args = [\"--x\"]\n", want: claudeWant("--x"), inRefused: true},
	{name: "claude-args-mixed", line: "Claude_Args = [\"--x\"]\n", out: "agent_args = [\"--x\"]\n", want: claudeWant("--x"), inRefused: true},
	{name: "claude-args-empty", line: "claude_args = []\n", out: "agent_args = []\n", want: claudeWant(), inRefused: true},
	{name: "claude-args-brackets-in-strings", line: "claude_args = [\"a]b\", \"c#d\"] # z\n", out: "agent_args = [\"a]b\", \"c#d\"] # z\n", want: claudeWant("a]b", "c#d"), inRefused: true},
	{name: "claude-args-quote-in-literal", line: "claude_args = ['a\"b']\n", out: "agent_args = ['a\"b']\n", want: claudeWant("a\"b"), inRefused: true},
	{name: "claude-args-escaped-quote", line: "claude_args = [\"a\\\"b\", \"c\"]\n", out: "agent_args = [\"a\\\"b\", \"c\"]\n", want: claudeWant("a\"b", "c"), inRefused: true},
	{name: "claude-args-triple-quoted-on-line", line: "claude_args = [\"\"\"a\"\"\", '''b''']\n", out: "agent_args = [\"\"\"a\"\"\", '''b''']\n", want: claudeWant("a", "b"), inRefused: true},
	// agent_args alone: no agent, so one is added (the loader needs it).
	{name: "agent-args", line: "agent_args = [\"--x\"]\n", want: claudeWant("--x"), inRefused: true},
	{name: "agent-args-mixed", line: "Agent_Args = [\"--x\"]\n", want: claudeWant("--x"), inRefused: true},
	{name: "agent-args-empty", line: "agent_args = []\n", want: claudeWant()},
	{name: "agent-args-twice", line: "agent_args = [\"a\"]\nagent_args = [\"b\"]\n", want: Want{LoaderRefuses: true}},
	// A line naming agent, in any letter case: nothing is added.
	{name: "agent-claude", line: "agent = \"claude\"\n", agent: true, want: Want{Agent: "claude"}},
	{name: "agent-codex", line: "agent = \"codex\"\n", agent: true, want: Want{Agent: "codex"}},
	{name: "agent-upper", line: "AGENT = \"codex\"\n", agent: true, want: Want{Agent: "codex"}},
	{name: "agent-mixed", line: "Agent = \"claude\"\n", agent: true, want: Want{Agent: "claude"}},
	{name: "agent-literal", line: "agent = 'codex'\n", agent: true, want: Want{Agent: "codex"}},
	{name: "agent-spaced", line: "  agent   =   \"codex\"   # c\n", agent: true, want: Want{Agent: "codex"}},
	{name: "agent-tight", line: "agent=\"codex\"#c\n", agent: true, want: Want{Agent: "codex"}},
	{name: "agent-empty", line: "agent = \"\"\n", agent: true, want: Want{}},
	{name: "agent-unknown", line: "agent = \"gemini\"\n", agent: true, want: Want{LoaderRefuses: true}},
	{name: "agent-not-a-string", line: "agent = 3\n", agent: true, want: Want{LoaderRefuses: true}},
	{name: "agent-twice", line: "agent = \"codex\"\nagent = \"claude\"\n", agent: true, want: Want{LoaderRefuses: true}},
	{name: "agent-with-agent-args", line: "agent = \"codex\"\nagent_args = [\"--x\"]\n", agent: true, want: Want{Agent: "codex", Args: []string{"--x"}}},
	{name: "agent-after-agent-args", line: "agent_args = [\"--x\"]\nAGENT = \"claude\"\n", agent: true, want: claudeWant("--x")},
	// agent beside claude_args: the agent stays, only the rename happens.
	{name: "agent-codex-with-claude-args", line: "agent = \"codex\"\nclaude_args = [\"--x\"]\n", out: "agent = \"codex\"\nagent_args = [\"--x\"]\n", agent: true, want: Want{Agent: "codex", Args: []string{"--x"}}, inRefused: true},
	{name: "agent-after-claude-args", line: "claude_args = [\"--x\"]\nagent = \"codex\"\n", out: "agent_args = [\"--x\"]\nagent = \"codex\"\n", agent: true, want: Want{Agent: "codex", Args: []string{"--x"}}, inRefused: true},
	{name: "agent-upper-with-claude-args", line: "AGENT = \"claude\"\nclaude_args = [\"--x\"]\n", out: "AGENT = \"claude\"\nagent_args = [\"--x\"]\n", agent: true, want: claudeWant("--x"), inRefused: true},
	{name: "agent-unknown-with-claude-args", line: "agent = \"gemini\"\nclaude_args = [\"--x\"]\n", out: "agent = \"gemini\"\nagent_args = [\"--x\"]\n", agent: true, want: Want{LoaderRefuses: true}, inRefused: true},
	// Lines that are not agent or the args, or only look like them.
	{name: "cwd", line: "cwd = \"/x\"\n", want: claudeWant()},
	{name: "cwd-hash-in-string", line: "cwd = \"/a#b\" # c\n", want: claudeWant()},
	{name: "cwd-literal-triple-on-line", line: "cwd = '''/x'''\n", want: claudeWant()},
	{name: "inline-table", line: "extra_env = { A = \"b\" }\n", want: claudeWant()},
	{name: "comment", line: "# c\n", want: claudeWant()},
	{name: "comment-names-keys", line: "# claude_args = [\"x\"]\n# agent = \"codex\"\n", want: claudeWant()},
	{name: "blank-lines", line: "\n  \n\t\n", want: claudeWant()},
	{name: "empty-value", line: "cwd =\n", want: Want{LoaderRefuses: true}},
	{name: "key-like-agent", line: "agent_extra = 1\nmy_agent = 1\n", want: Want{LoaderRefuses: true}},
	{name: "key-like-claude-args", line: "claude_args_x = 1\nxclaude_args = 1\nclaude-args = 1\n", want: Want{LoaderRefuses: true}},
}

// templateShapeCases runs every topShape in the top-level part, before a table
// the migration must leave alone and with no table at all.
func templateShapeCases() []RenameCase {
	var cases []RenameCase
	for _, s := range topShapes {
		out := s.out
		if out == "" {
			out = s.line
		}
		for _, layout := range []struct{ name, tail string }{{"with-table", labelsTail}, {"bare", ""}} {
			c := RenameCase{Name: "template-top-" + s.name + "/" + layout.name, Kind: KindTemplate, In: s.line + layout.tail, Want: s.want}
			if layout.tail != "" && !s.want.LoaderRefuses {
				c.Want.Labels = ownerLabels
			}
			added := ""
			if !s.agent {
				added = agentLine
			}
			c.Out = out + added + layout.tail
			if c.Out == c.In {
				c.Outcome = Unchanged
			} else {
				c.Outcome = Migrated
				c.InRefused = s.inRefused
			}
			cases = append(cases, c)
		}
	}
	return cases
}

// ---- templates: the same lines in a table ----

// templateTableCases puts lines under [labels], where the migration never
// reads: only the agent line is added (before the table), even for lines no
// reader of the top-level part would accept.
func templateTableCases() []RenameCase {
	type tbl struct {
		name, body string
		want       Want
	}
	loads := func(kv ...string) Want {
		w := Want{Agent: "claude", Labels: map[string]string{}}
		for i := 0; i < len(kv); i += 2 {
			w.Labels[kv[i]] = kv[i+1]
		}
		return w
	}
	refused := Want{LoaderRefuses: true}
	shapes := []tbl{
		{"claude-args", "claude_args = \"x\"\n", loads("claude_args", "x")},
		{"claude-args-upper", "CLAUDE_ARGS = \"x\"\n", loads("CLAUDE_ARGS", "x")},
		{"claude-args-twice", "claude_args = \"a\"\nclaude_args = \"b\"\n", refused},
		{"claude-args-and-agent-args", "claude_args = \"a\"\nagent_args = \"b\"\n", loads("claude_args", "a", "agent_args", "b")},
		{"agent-args", "agent_args = \"x\"\n", loads("agent_args", "x")},
		{"agent", "agent = \"codex\"\n", loads("agent", "codex")},
		{"agent-upper", "AGENT = \"x\"\n", loads("AGENT", "x")},
		{"quoted-key", "\"claude_args\" = \"x\"\n", loads("claude_args", "x")},
		{"literal-quoted-key", "'cwd' = \"x\"\n", loads("cwd", "x")},
		{"dotted-key", "claude_args.x = \"c\"\n", refused},
		{"multi-line-array", "claude_args = [\n\"a\",\n]\n", refused},
		{"multi-line-string", "claude_args = \"\"\"\nx\"\"\"\n", loads("claude_args", "x")},
		{"multi-line-literal-string", "x = '''\ny\n'''\n", loads("x", "y\n")},
		{"junk", "junk\n", refused},
		{"comment", "# claude_args = \"x\"\n", loads()},
	}
	var cases []RenameCase
	for _, s := range shapes {
		in := "[labels]\n" + s.body
		cases = append(cases, RenameCase{
			Name: "template-table-" + s.name, Kind: KindTemplate, In: in,
			Outcome: Migrated, Out: agentLine + in, Want: s.want,
		})
	}
	// The same table, after a top-level part that is renamed and has its agent.
	cases = append(cases, RenameCase{
		Name: "template-table-claude-args-below-top-level-claude-args", Kind: KindTemplate,
		In:      "claude_args = [\"--top\"]\n[labels]\nclaude_args = \"x\"\n",
		Outcome: Migrated, Out: "agent_args = [\"--top\"]\n" + agentLine + "[labels]\nclaude_args = \"x\"\n",
		Want:      Want{Agent: "claude", Args: []string{"--top"}, Labels: map[string]string{"claude_args": "x"}},
		InRefused: true,
	})
	cases = append(cases, RenameCase{
		Name: "template-table-agent-below-agent", Kind: KindTemplate,
		In:      "agent = \"codex\"\n[labels]\nagent = \"x\"\n",
		Outcome: Unchanged, Want: Want{Agent: "codex", Labels: map[string]string{"agent": "x"}},
	})
	// Lines no top-level reader accepts, below a header: never read.
	cases = append(cases, RenameCase{
		Name: "template-table-unreadable-lines-below-a-header", Kind: KindTemplate,
		In:      "cwd = \"/x\"\n[permissions]\nallow = [\n  \"a\",\n  \"b\",\n]\n[labels]\n\"q\" = \"1\"\na = \"\"\"\nb\n\"\"\"\n",
		Outcome: Migrated,
		Out:     "cwd = \"/x\"\n" + agentLine + "[permissions]\nallow = [\n  \"a\",\n  \"b\",\n]\n[labels]\n\"q\" = \"1\"\na = \"\"\"\nb\n\"\"\"\n",
		Want:    Want{Agent: "claude", Labels: map[string]string{"q": "1", "a": "b\n"}},
	})
	return cases
}

// ---- templates: what ends the top-level part ----

// templateHeaderCases ends the top-level part with each header form: the
// agent line goes immediately before it, and the table is kept.
func templateHeaderCases() []RenameCase {
	type hdr struct {
		name, header, body string
		want               Want
	}
	body := "owner = \"me\"\n"
	shapes := []hdr{
		{"bare", "[labels]\n", body, Want{Labels: ownerLabels}},
		{"spaces", "[ labels ]\n", body, Want{Labels: ownerLabels}},
		{"leading-blanks", "   \t[labels]\n", body, Want{Labels: ownerLabels}},
		{"upper", "[LABELS]\n", body, Want{Labels: ownerLabels}},
		{"comment", "[labels] # the [table]\n", body, Want{Labels: ownerLabels}},
		{"quoted", "[\"labels\"]\n", body, Want{Labels: ownerLabels}},
		{"literal-quoted", "['labels']\n", body, Want{Labels: ownerLabels}},
		{"dotted", "[labels.x]\n", "k = \"v\"\n", Want{LoaderRefuses: true}},
		{"array-of-tables", "[[permissions]]\n", "allow = [\"a\"]\n", Want{LoaderRefuses: true}},
		{"array-of-tables-labels", "[[labels]]\n", body, Want{LoaderRefuses: true}},
		{"inline-array-looking", "[ \"a\", \"b\" ]\n", "", Want{LoaderRefuses: true}},
	}
	var cases []RenameCase
	for _, s := range shapes {
		for _, top := range []struct{ name, in, out string }{
			{"no-agent", "cwd = \"/x\"\n", "cwd = \"/x\"\n" + agentLine},
			{"claude-args", "claude_args = [\"--x\"]\n\n# c\n", "agent_args = [\"--x\"]\n\n# c\n" + agentLine},
			{"agent", "agent = \"codex\"\n", "agent = \"codex\"\n"},
			{"empty", "", agentLine},
		} {
			c := RenameCase{Name: "template-header-" + s.name + "/" + top.name, Kind: KindTemplate, In: top.in + s.header + s.body, Want: s.want, Outcome: Migrated, Out: top.out + s.header + s.body}
			switch top.name {
			case "claude-args":
				c.InRefused = true
				c.Want.Agent, c.Want.Args = "claude", []string{"--x"}
			case "agent":
				c.Want.Agent = "codex"
			default:
				c.Want.Agent = "claude"
			}
			if s.want.LoaderRefuses {
				c.Want = Want{LoaderRefuses: true}
			}
			if c.Out == c.In {
				c.Outcome = Unchanged
			}
			cases = append(cases, c)
		}
	}
	// The header is the last line of a file with no final line end.
	cases = append(cases, RenameCase{
		Name: "template-header-last-line-no-final-newline", Kind: KindTemplate,
		In: "cwd = \"/x\"\n[labels]", Outcome: Migrated, Out: "cwd = \"/x\"\n" + agentLine + "[labels]",
		Want: Want{Agent: "claude", Labels: map[string]string{}},
	})
	// A header-like line is a header however its first character is reached.
	cases = append(cases, RenameCase{
		Name: "template-header-after-blank-lines-and-comments", Kind: KindTemplate,
		In: "\n# a\n\n[labels]\nowner = \"me\"\n", Outcome: Migrated, Out: "\n# a\n\n" + agentLine + "[labels]\nowner = \"me\"\n",
		Want: Want{Agent: "claude", Labels: ownerLabels},
	})
	return cases
}

// ---- templates: file forms ----

// templateFormCases varies the file's form around three base templates: the
// agent line takes the file's line end (CRLF when its first line end is
// CRLF), a byte-order mark stays first, and a missing final line end is
// handled.
func templateFormCases() []RenameCase {
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	var cases []RenameCase
	add := func(name, in, out string, want Want, inRefused bool) {
		cases = append(cases, RenameCase{Name: "template-form-" + name, Kind: KindTemplate, In: in, Outcome: Migrated, Out: out, Want: want, InRefused: inRefused})
	}
	withArgs := claudeWant("--x")
	withArgs.Labels = ownerLabels
	const (
		tableIn  = "# c\nclaude_args = [\"--x\"]\n\n[labels]\nowner = \"me\"\n"
		tableOut = "# c\nagent_args = [\"--x\"]\n\n" + agentLine + "[labels]\nowner = \"me\"\n"
		bareIn   = "# c\nclaude_args = [\"--x\"]\n"
		bareOut  = "# c\nagent_args = [\"--x\"]\n" + agentLine
		firstIn  = "[labels]\nowner = \"me\"\n"
		firstOut = agentLine + firstIn
	)
	bareWant := claudeWant("--x")
	firstWant := Want{Agent: "claude", Labels: ownerLabels}

	add("lf-table", tableIn, tableOut, withArgs, true)
	add("lf-bare", bareIn, bareOut, bareWant, true)
	add("lf-header-first", firstIn, firstOut, firstWant, false)
	add("crlf-table", crlf(tableIn), crlf(tableOut), withArgs, true)
	add("crlf-bare", crlf(bareIn), crlf(bareOut), bareWant, true)
	add("crlf-header-first", crlf(firstIn), crlf(firstOut), firstWant, false)
	add("bom-table", utf8BOM+tableIn, utf8BOM+tableOut, withArgs, true)
	add("bom-bare", utf8BOM+bareIn, utf8BOM+bareOut, bareWant, true)
	add("bom-header-first", utf8BOM+firstIn, utf8BOM+firstOut, firstWant, false)
	add("bom-claude-args-first-line", utf8BOM+"claude_args = [\"--x\"]\n", utf8BOM+"agent_args = [\"--x\"]\n"+agentLine, bareWant, true)
	add("bom-crlf-table", utf8BOM+crlf(tableIn), utf8BOM+crlf(tableOut), withArgs, true)
	add("bom-crlf-header-first", utf8BOM+crlf(firstIn), utf8BOM+crlf(firstOut), firstWant, false)
	// No final line end: the file ends on its last line as it was, unless the
	// agent line goes after it.
	add("no-final-newline-table", strings.TrimSuffix(tableIn, "\n"), strings.TrimSuffix(tableOut, "\n"), withArgs, true)
	add("no-final-newline-bare", strings.TrimSuffix(bareIn, "\n"), bareOut, bareWant, true)
	add("no-final-newline-header-first", strings.TrimSuffix(firstIn, "\n"), strings.TrimSuffix(firstOut, "\n"), firstWant, false)
	add("no-final-newline-crlf-bare", strings.TrimSuffix(crlf(bareIn), "\r\n"), crlf(bareOut), bareWant, true)
	add("no-final-newline-crlf-table", strings.TrimSuffix(crlf(tableIn), "\r\n"), strings.TrimSuffix(crlf(tableOut), "\r\n"), withArgs, true)
	add("no-final-newline-bom-bare", utf8BOM+strings.TrimSuffix(bareIn, "\n"), utf8BOM+bareOut, bareWant, true)
	add("crlf-single-line", "cwd = \"/x\"\r\n", "cwd = \"/x\"\r\n"+strings.TrimSuffix(agentLine, "\n")+"\r\n", claudeWant(), false)
	// Mixed line ends: the first line end sets the added line's.
	add("lf-first-then-crlf", "cwd = \"/x\"\nclaude_args = [\"a\"]\r\n[labels]\r\nowner = \"me\"\r\n",
		"cwd = \"/x\"\nagent_args = [\"a\"]\r\n"+agentLine+"[labels]\r\nowner = \"me\"\r\n",
		Want{Agent: "claude", Args: []string{"a"}, Labels: ownerLabels}, true)
	add("crlf-first-then-lf", "cwd = \"/x\"\r\nclaude_args = [\"a\"]\n[labels]\nowner = \"me\"\n",
		"cwd = \"/x\"\r\nagent_args = [\"a\"]\n"+strings.TrimSuffix(agentLine, "\n")+"\r\n[labels]\nowner = \"me\"\n",
		Want{Agent: "claude", Args: []string{"a"}, Labels: ownerLabels}, true)
	add("crlf-first-no-final-newline", "cwd = \"/x\"\r\nclaude_args = [\"a\"]",
		"cwd = \"/x\"\r\nagent_args = [\"a\"]\r\n"+strings.TrimSuffix(agentLine, "\n")+"\r\n",
		claudeWant("a"), true)
	// Files with next to nothing in them.
	add("empty-file", "", agentLine, claudeWant(), false)
	add("only-newline", "\n", "\n"+agentLine, claudeWant(), false)
	add("only-comment-no-newline", "# c", "# c\n"+agentLine, claudeWant(), false)
	add("only-bom", utf8BOM, utf8BOM+"\n"+agentLine, claudeWant(), false)
	add("only-bom-newline", utf8BOM+"\n", utf8BOM+"\n"+agentLine, claudeWant(), false)
	cases = append(cases,
		RenameCase{Name: "template-form-missing-file", Kind: KindTemplate, Missing: true, Outcome: Unchanged},
		RenameCase{Name: "template-form-crlf-already-migrated", Kind: KindTemplate, In: crlf("agent = \"codex\"\nagent_args = [\"--x\"]\n" + labelsTail), Outcome: Unchanged, Want: Want{Agent: "codex", Args: []string{"--x"}, Labels: ownerLabels}},
		RenameCase{Name: "template-form-bom-already-migrated", Kind: KindTemplate, In: utf8BOM + "agent = \"claude\"\n" + labelsTail, Outcome: Unchanged, Want: Want{Agent: "claude", Labels: ownerLabels}},
		RenameCase{Name: "template-form-no-final-newline-already-migrated", Kind: KindTemplate, In: "agent = \"claude\"", Outcome: Unchanged, Want: Want{Agent: "claude"}},
	)
	return cases
}

// ---- templates: the rule rows ----

// templateRuleCases are the rules the other groups only touch: an agent key
// that is only inside a table does not count, claude_args in a table is not
// renamed, and only the first header ends the top-level part.
func templateRuleCases() []RenameCase {
	return []RenameCase{
		{
			Name: "template-rule-agent-only-inside-a-table-still-gets-the-insert", Kind: KindTemplate,
			In: "claude_args = [\"--x\"]\n[labels]\nagent = \"codex\"\n", Outcome: Migrated,
			Out:  "agent_args = [\"--x\"]\n" + agentLine + "[labels]\nagent = \"codex\"\n",
			Want: Want{Agent: "claude", Args: []string{"--x"}, Labels: map[string]string{"agent": "codex"}}, InRefused: true,
		},
		{
			Name: "template-rule-claude-args-inside-a-table-is-untouched", Kind: KindTemplate,
			In: "agent = \"claude\"\n[labels]\nclaude_args = \"x\"\n", Outcome: Unchanged,
			Want: Want{Agent: "claude", Labels: map[string]string{"claude_args": "x"}},
		},
		{
			Name: "template-rule-only-the-first-table-header-ends-the-top-level-part", Kind: KindTemplate,
			In: "cwd = \"/x\"\n[labels]\nowner = \"me\"\n[extra_env]\nclaude_args = \"x\"\nagent = \"y\"\n", Outcome: Migrated,
			Out:  "cwd = \"/x\"\n" + agentLine + "[labels]\nowner = \"me\"\n[extra_env]\nclaude_args = \"x\"\nagent = \"y\"\n",
			Want: Want{Agent: "claude", Labels: ownerLabels},
		},
	}
}

// ---- templates: refusals ----

func templateRefusalCases() []RenameCase {
	// unreadable: a top-level line the migration cannot read; line is its
	// 1-based number in the file, text what the message shows for it.
	unreadable := func(name, in string, line, text string) RenameCase {
		return RenameCase{Name: "template-refused-" + name, Kind: KindTemplate, In: in, Outcome: Refused,
			Headline: headlineUnreadable, Show: []string{"  line " + line + "  : " + text}}
	}
	return []RenameCase{
		// A line outside the form: a quoted or dotted name, a value that goes on
		// to the next line, a line that is none of blank, comment or name = value.
		unreadable("quoted-key", "\"cwd\" = \"/x\"\n", "1", "\"cwd\" = \"/x\""),
		unreadable("quoted-claude-args", "\"claude_args\" = [\"--x\"]\n", "1", "\"claude_args\" = [\"--x\"]"),
		unreadable("literal-quoted-key", "'cwd' = \"/x\"\n", "1", "'cwd' = \"/x\""),
		unreadable("quoted-agent", "\"agent\" = \"codex\"\nclaude_args = [\"--x\"]\n", "1", "\"agent\" = \"codex\""),
		unreadable("dotted-key", "a.b = 1\n", "1", "a.b = 1"),
		unreadable("dotted-claude-args", "claude_args.x = 1\n", "1", "claude_args.x = 1"),
		unreadable("dotted-agent", "agent.x = \"claude\"\n", "1", "agent.x = \"claude\""),
		unreadable("multi-line-array", "claude_args = [\n  \"--x\",\n]\n", "1", "claude_args = ["),
		unreadable("multi-line-array-after-comment", "claude_args = [ # c\n\"--x\"\n]\n", "1", "claude_args = [ # c"),
		unreadable("multi-line-array-other-key", "cwd = \"/x\"\nx = [\n1,\n]\n", "2", "x = ["),
		unreadable("multi-line-inline-table", "extra_env = {\nA = \"b\"\n}\n", "1", "extra_env = {"),
		unreadable("multi-line-basic-string", "cwd = \"\"\"\n/x\n\"\"\"\n", "1", "cwd = \"\"\""),
		unreadable("multi-line-literal-string", "cwd = '''\n/x\n'''\n", "1", "cwd = '''"),
		unreadable("multi-line-string-closed-on-a-later-line", "cwd = \"\"\"/x\n\"\"\"\n", "1", "cwd = \"\"\"/x"),
		unreadable("junk-line", "junk\n", "1", "junk"),
		unreadable("equals-only", "= 3\n", "1", "= 3"),
		unreadable("space-in-name", "claude args = 1\n", "1", "claude args = 1"),
		unreadable("non-ascii-name", "é = 1\n", "1", "é = 1"),
		unreadable("unterminated-string", "cwd = \"/x\n", "1", "cwd = \"/x"),
		unreadable("unterminated-literal-string", "cwd = '/x\n", "1", "cwd = '/x"),
		unreadable("unclosed-array", "claude_args = [\"a\"\n", "1", "claude_args = [\"a\""),
		unreadable("bad-line-after-good-ones", "# c\n\ncwd = \"/x\"\njunk\n[labels]\n", "4", "junk"),
		unreadable("first-bad-line-named", "cwd = \"/x\"\na.b = 1\njunk\n", "2", "a.b = 1"),
		unreadable("crlf", "cwd = \"/x\"\r\na.b = 1\r\n", "2", "a.b = 1"),
		unreadable("bom", utf8BOM+"a.b = 1\n", "1", "a.b = 1"),
		unreadable("bad-line-before-a-claude-args-twice", "junk\nclaude_args = [\"a\"]\nclaude_args = [\"b\"]\n", "1", "junk"),
		// claude_args set twice, or beside agent_args.
		{Name: "template-refused-claude-args-twice", Kind: KindTemplate, In: "claude_args = [\"a\"]\nclaude_args = [\"b\"]\n", Outcome: Refused,
			Headline: headlineClaudeTwo, Show: []string{"  line 1  : claude_args = [\"a\"]", "  line 2  : claude_args = [\"b\"]"}},
		{Name: "template-refused-claude-args-twice-other-case", Kind: KindTemplate, In: "agent = \"codex\"\nclaude_args = [\"a\"]\nCLAUDE_ARGS = [\"b\"]\n" + labelsTail, Outcome: Refused,
			Headline: headlineClaudeTwo, Show: []string{"  line 2  : claude_args = [\"a\"]", "  line 3  : CLAUDE_ARGS = [\"b\"]"}},
		{Name: "template-refused-claude-args-and-agent-args", Kind: KindTemplate, In: "claude_args = [\"a\"]\nagent_args = [\"b\"]\n", Outcome: Refused,
			Headline: headlineBoth, Show: []string{"  line 1  : claude_args = [\"a\"]", "  line 2  : agent_args = [\"b\"]"}},
		{Name: "template-refused-agent-args-then-claude-args", Kind: KindTemplate, In: "agent = \"claude\"\nagent_args = [\"b\"]\nclaude_args = [\"a\"]\n", Outcome: Refused,
			Headline: headlineBoth, Show: []string{"  line 2  : agent_args = [\"b\"]", "  line 3  : claude_args = [\"a\"]"}},
		{Name: "template-refused-claude-args-and-agent-args-other-case", Kind: KindTemplate, In: "CLAUDE_ARGS = [\"a\"]\nAgent_Args = [\"b\"]\n", Outcome: Refused,
			Headline: headlineBoth, Show: []string{"  line 1  : CLAUDE_ARGS = [\"a\"]", "  line 2  : Agent_Args = [\"b\"]"}},
		{Name: "template-refused-claude-args-twice-and-agent-args", Kind: KindTemplate, In: "claude_args = [\"a\"]\nclaude_args = [\"b\"]\nagent_args = [\"c\"]\n", Outcome: Refused,
			Headline: headlineClaudeTwo, Show: []string{"  line 1  : claude_args = [\"a\"]", "  line 2  : claude_args = [\"b\"]", "  line 3  : agent_args = [\"c\"]"}},
		{Name: "template-refused-claude-args-twice-crlf", Kind: KindTemplate, In: "claude_args = [\"a\"]\r\nclaude_args = [\"b\"]\r\n", Outcome: Refused,
			Headline: headlineClaudeTwo, Show: []string{"  line 1  : claude_args = [\"a\"]", "  line 2  : claude_args = [\"b\"]"}},
	}
}
