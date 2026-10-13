package spawn

import "strings"

type claudeArity uint8

const (
	claudeFlag claudeArity = iota
	claudeRequired
	claudeOptional
)

type claudeOptionSpec struct {
	canonical string
	arity     claudeArity
	variadic  bool
}

// ArgOption retains one option's bare spelling, canonical name, original token,
// position and values. ValueIndices contains only detached argv positions;
// attached values remain in Spelling. ClusterStart/End bound the option's bytes
// within a short cluster (including its attached value), or are zero for a
// whole-token option. Known distinguishes verified options from AD's valueless
// fallback for future options. After marks Codex's policy reading after --;
// those tokens are operands in the actual parser, not extractable options.
type ArgOption struct {
	Index        int
	Name         string
	Canonical    string
	Spelling     string
	Values       []string
	ValueIndices []int
	ClusterStart int
	ClusterEnd   int
	Known        bool
	After        bool
}

// ClaudeArgs is a complete reading of the original list, without validation.
// Positionals and DoubleDash refer to original argv positions. Required values
// own even option-shaped tokens and --; they are never independent options.
type ClaudeArgs struct {
	Options     []ArgOption
	Positionals []int
	DoubleDash  int
}

// ReadClaudeArgs uses the verified root option table and Commander semantics:
// required/optional/list values, exact short names before clusters, and attached
// short values with their leading equals retained. Unknown options are read as
// valueless under AD's future-option policy, not as a claim about upstream's
// unknown-option acceptance. It does no I/O and never fails.
func ReadClaudeArgs(args []string) ClaudeArgs {
	out := ClaudeArgs{DoubleDash: -1}
	for i := 0; i < len(args); {
		tok := args[i]
		if tok == "--" {
			out.DoubleDash = i
			for p := i + 1; p < len(args); p++ {
				out.Positionals = append(out.Positionals, p)
			}
			break
		}
		if !claudeOptionToken(tok) {
			out.Positionals = append(out.Positionals, i)
			i++
			continue
		}
		if spec, known := claudeOptionTable[tok]; known {
			o := ArgOption{Index: i, Name: tok, Canonical: spec.canonical, Spelling: tok, Known: true}
			i = claudeTakeValues(args, i+1, spec, &o)
			out.Options = append(out.Options, o)
			continue
		}
		if strings.HasPrefix(tok, "--") {
			name, value, inline := strings.Cut(tok, "=")
			spec, known := claudeOptionTable[name]
			o := ArgOption{Index: i, Name: name, Canonical: name, Spelling: tok, Known: known}
			next := i + 1
			if known {
				o.Canonical = spec.canonical
				if inline && spec.arity != claudeFlag {
					o.Values = []string{value}
				} else if !inline {
					next = claudeTakeValues(args, next, spec, &o)
				}
			}
			out.Options = append(out.Options, o)
			i = next
			continue
		}
		i = out.readShort(args, i)
	}
	return out
}

func claudeOptionToken(s string) bool { return len(s) > 1 && s[0] == '-' }

func claudeTakeValues(args []string, next int, spec claudeOptionSpec, o *ArgOption) int {
	if spec.arity == claudeFlag || next == len(args) {
		return next
	}
	if spec.arity == claudeOptional && claudeOptionToken(args[next]) {
		return next
	}
	o.Values = append(o.Values, args[next])
	o.ValueIndices = append(o.ValueIndices, next)
	next++
	for spec.variadic && next < len(args) && !claudeOptionToken(args[next]) {
		o.Values = append(o.Values, args[next])
		o.ValueIndices = append(o.ValueIndices, next)
		next++
	}
	return next
}

func (c *ClaudeArgs) readShort(args []string, i int) int {
	tok, next := args[i], i+1
	for p := 1; p < len(tok); p++ {
		name := "-" + tok[p:p+1]
		spec, known := claudeOptionTable[name]
		o := ArgOption{Index: i, Name: name, Canonical: name, Spelling: tok, Known: known, ClusterStart: p, ClusterEnd: p + 1}
		if !known {
			// Commander leaves an unknown remainder intact: -xc is not -x
			// plus continue. AD still treats that unknown token as valueless,
			// so a separate following -c cannot be swallowed by it.
			o.Name = "-" + tok[p:]
			o.Canonical = o.Name
			o.ClusterEnd = len(tok)
			c.Options = append(c.Options, o)
			break
		}
		if known {
			o.Canonical = spec.canonical
		}
		if known && spec.arity != claudeFlag {
			o.ClusterEnd = len(tok)
			if p+1 < len(tok) {
				o.Values = []string{tok[p+1:]}
			} else {
				next = claudeTakeValues(args, next, spec, &o)
			}
			c.Options = append(c.Options, o)
			break
		}
		c.Options = append(c.Options, o)
	}
	return next
}

// HasPrint examines the full parsed list before any selector extraction.
func (c ClaudeArgs) HasPrint() bool {
	for _, o := range c.Options {
		if o.Canonical == "--print" {
			return true
		}
	}
	return false
}
