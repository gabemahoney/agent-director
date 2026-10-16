package spawn

import (
	"slices"
	"strings"
)

// ClaudeResumeArgs is the single set of canonical existing-session selectors
// and exclusive dependents verified at Claude 2.1.280 and 2.1.296. Short aliases
// are canonicalized by ReadClaudeArgs. --session-id creates a new session.
var ClaudeResumeArgs = map[string]bool{
	"--resume": true, "--continue": true, "--fork-session": true,
	"--from-pr": true, "--teleport": true, "--resume-session-at": true,
	"--resume-drops-turn": true, "--rewind-files": true, "--reply-on-resume": true,
	"--cloud": true, "--remote": true, "--attach-serve": true,
}

// CodexResumeArgs is the single command/selector set verified independently at
// Codex 0.156.0 and 0.162.1. include-non-interactive belongs to resume only;
// command position and original session/prompt roles come from ReadCodexArgs.
var CodexResumeArgs = map[string]bool{
	"resume": true, "fork": true, "--last": true, "--all": true,
	"--include-non-interactive": true,
}

// CursorResumeArgs contains the session selectors verified in the versioned
// Cursor 2026.09.28-64d2043 command definition. The hidden new-session-id is
// included because AD owns the selected conversation on replay.
var CursorResumeArgs = map[string]bool{
	"--resume": true, "--continue": true, "--new-session-id": true, "resume": true,
}

// ExtractedArgs carries copied survivors, original dropped-token spellings in
// argv order, and the parsed options removed. A partially removed cluster is
// reported with its original token spelling; Removed identifies the exact
// removed bytes. For Claude inputs with all required values, Args preserves
// surviving parsed roles, re-encoding an altered Claude cluster or moving prompts
// behind -- only when literal removal changes
// their meaning. Inputs missing a recognized required value get literal removal
// only, leaving rejection to the agent parser. These rendering changes add no
// removal metadata. No caller's slice is changed.
// Exact long names owned as another option's value remain intact for Task 5's
// refusal policy; extraction never independently removes option-owned values.
type ExtractedArgs struct {
	Args    []string
	Dropped []string
	Removed []ArgOption
}

// ExtractResumeArgs is shared by replay and switching-spawn policy. It removes
// only parsed selectors, their owned values, and approved orphaned cloud
// controls. Print is deliberately separate: spawn must inspect the full list
// before extracting, whereas replay removes print to relaunch interactively.
func ExtractResumeArgs(agent string, args []string) ExtractedArgs {
	if agent == "claude" {
		return extractClaudeArgs(args, false)
	}
	if agent == "codex" {
		return extractCodexArgs(args)
	}
	if agent == "cursor" {
		return extractCursorArgs(args)
	}
	return ExtractedArgs{Args: append([]string(nil), args...)}
}

// Cursor selectors are verified in Cursor 2026.09.28-64d2043's versioned
// command definition. Required model values and operands after -- own their
// tokens, including spellings which would otherwise be selectors.
func extractCursorArgs(args []string) ExtractedArgs {
	var removed []ArgOption
	commandPosition := true
	for i := 0; i < len(args); i++ {
		key, value, equals := strings.Cut(args[i], "=")
		if key == "--" {
			break
		}
		if key == "--model" || key == "-m" {
			if !equals && i+1 < len(args) {
				i++
			}
			continue
		}
		if !CursorResumeArgs[key] || (key == "resume" && (equals || !commandPosition)) {
			if !strings.HasPrefix(args[i], "-") {
				commandPosition = false
			}
			continue
		}
		if key == "resume" {
			commandPosition = false
		}
		o := ArgOption{Index: i, Name: key, Canonical: key, Spelling: args[i], Known: true}
		if equals {
			o.Values = []string{value}
		} else if (key == "--new-session-id" || (key == "--resume" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"))) && i+1 < len(args) {
			i++
			o.Values = []string{args[i]}
			o.ValueIndices = []int{i}
		}
		removed = append(removed, o)
	}
	return removeArgOptions(args, removed, nil)
}

func extractClaudeArgs(args []string, print bool) ExtractedArgs {
	read := ReadClaudeArgs(args)
	cloud, context := false, false
	for _, o := range read.Options {
		cloud = cloud || o.Canonical == "--cloud" || o.Canonical == "--remote"
		if (o.Canonical == "--environment" || o.Canonical == "--pool") && len(o.Values) == 1 {
			context = true
		}
	}
	var removed []ArgOption
	for _, o := range read.Options {
		orphan := cloud && !context && (o.Canonical == "--forward-home-settings" || o.Canonical == "--ref" || o.Canonical == "--on-branch")
		if ClaudeResumeArgs[o.Canonical] || orphan || (print && o.Canonical == "--print") {
			removed = append(removed, o)
		}
	}
	return renderClaudeArgs(args, read, removed)
}

// renderClaudeArgs checks the literal survivors against their original reading.
// An original missing required value bypasses all repairs: an inserted delimiter
// could become that option's value instead of protecting the original operands.
// Removing an option boundary can expose a prompt to an optional/list value;
// keep every surviving option group ahead of the protected original operands
// only in that case. An option-owned -- never participates in this movement.
func renderClaudeArgs(args []string, read ClaudeArgs, removed []ArgOption) ExtractedArgs {
	for _, o := range read.Options {
		if spec, known := claudeOptionTable[o.Name]; known && spec.arity == claudeRequired && len(o.Values) == 0 {
			return removeArgOptions(args, removed, nil)
		}
	}
	surviving := survivingClaudeOptions(read.Options, removed)
	out := renderArgOptions(args, removed, nil, surviving)
	filtered := ReadClaudeArgs(out.Args)
	if sameClaudeMeaning(surviving, filtered.Options) &&
		sameClaudeOperands(args, read.Positionals, out.Args, filtered.Positionals) {
		return out
	}
	move := make(map[int]bool)
	for _, p := range read.Positionals {
		move[p] = true
	}
	if read.DoubleDash >= 0 {
		move[read.DoubleDash] = true
	}
	out.Args = renderArgOptions(args, removed, move, surviving).Args
	if len(read.Positionals) > 0 || read.DoubleDash >= 0 {
		out.Args = append(out.Args, "--")
		for _, p := range read.Positionals {
			out.Args = append(out.Args, args[p])
		}
	}
	return out
}

func survivingClaudeOptions(options, removed []ArgOption) []ArgOption {
	drop := make(map[[2]int]bool, len(removed))
	for _, o := range removed {
		drop[[2]int{o.Index, o.ClusterStart}] = true
	}
	var surviving []ArgOption
	for _, o := range options {
		if !drop[[2]int{o.Index, o.ClusterStart}] {
			surviving = append(surviving, o)
		}
	}
	return surviving
}

// Positions and spellings may change during rendering; identity and owned
// values must not. This also retains AD's valueless reading of unknown options.
func sameClaudeMeaning(want, got []ArgOption) bool {
	if len(want) != len(got) {
		return false
	}
	for i, o := range want {
		if o.Canonical != got[i].Canonical || o.Known != got[i].Known || !slices.Equal(o.Values, got[i].Values) {
			return false
		}
	}
	return true
}

func sameClaudeOperands(original []string, originalPositions []int, rendered []string, renderedPositions []int) bool {
	if len(originalPositions) != len(renderedPositions) {
		return false
	}
	for i, p := range originalPositions {
		if original[p] != rendered[renderedPositions[i]] {
			return false
		}
	}
	return true
}

func extractCodexArgs(args []string) ExtractedArgs {
	read := ReadCodexArgs(args)
	if read.Command < 0 || !CodexResumeArgs[args[read.Command]] {
		return ExtractedArgs{Args: append([]string(nil), args...)}
	}
	command := args[read.Command]
	var removed []ArgOption
	for _, o := range read.Options {
		if o.After || !CodexResumeArgs[o.Canonical] || (command == "fork" && o.Canonical == "--include-non-interactive") {
			continue
		}
		removed = append(removed, o)
	}
	extra := map[int]bool{read.Command: true}
	if read.SessionID >= 0 {
		extra[read.SessionID] = true
	}
	var prompts []int
	for _, p := range read.Positionals {
		if p <= read.Command {
			continue
		}
		if p != read.SessionID {
			prompts = append(prompts, p)
		}
	}
	// Move original prompt tokens behind an unowned delimiter without joining
	// them. Values that resemble commands or flags retain their operand role.
	move := make(map[int]bool)
	for _, p := range prompts {
		move[p] = true
	}
	if read.DoubleDash >= 0 {
		move[read.DoubleDash] = true
	}
	filtered := removeArgOptions(args, removed, extra)
	// Rebuild with original positions, since removed tokens shifted indices.
	withMove := make(map[int]bool)
	for p := range extra {
		withMove[p] = true
	}
	for p := range move {
		withMove[p] = true
	}
	filtered.Args = removeArgOptions(args, removed, withMove).Args
	if len(prompts) > 0 || read.DoubleDash >= 0 {
		filtered.Args = append(filtered.Args, "--")
		for _, p := range prompts {
			filtered.Args = append(filtered.Args, args[p])
		}
	}
	return filtered
}

func removeArgOptions(args []string, removed []ArgOption, extra map[int]bool) ExtractedArgs {
	return renderArgOptions(args, removed, extra, nil)
}

func renderArgOptions(args []string, removed []ArgOption, extra map[int]bool, claudeSurvivors []ArgOption) ExtractedArgs {
	whole := make(map[int]bool)
	parts := make(map[int][]bool)
	for p := range extra {
		whole[p] = true
	}
	for _, o := range removed {
		for _, p := range o.ValueIndices {
			whole[p] = true
		}
		if o.ClusterStart == 0 {
			whole[o.Index] = true
			continue
		}
		if parts[o.Index] == nil {
			parts[o.Index] = make([]bool, len(args[o.Index]))
		}
		for p := o.ClusterStart; p < o.ClusterEnd; p++ {
			parts[o.Index][p] = true
		}
	}
	out := ExtractedArgs{Removed: removed}
	for i, tok := range args {
		if whole[i] {
			out.Dropped = append(out.Dropped, tok)
			continue
		}
		if mask := parts[i]; mask != nil {
			var keep strings.Builder
			keep.WriteByte('-')
			for p := 1; p < len(tok); p++ {
				if !mask[p] {
					keep.WriteByte(tok[p])
				}
			}
			out.Dropped = append(out.Dropped, tok)
			if keep.Len() > 1 {
				out.Args = append(out.Args, renderClaudeCluster(args, i, keep.String(), claudeSurvivors)...)
			}
			continue
		}
		out.Args = append(out.Args, tok)
	}
	return out
}

// Exact registered short tokens win over cluster parsing. Literal deletion
// from -cd2e would produce -d2e, changing debug's attached value into a flag.
// Re-encode only a changed cluster whose surviving identity/values differ.
func renderClaudeCluster(args []string, index int, literal string, surviving []ArgOption) []string {
	var want []ArgOption
	probe := []string{literal}
	for _, o := range surviving {
		if o.Index != index {
			continue
		}
		want = append(want, o)
		for _, p := range o.ValueIndices {
			probe = append(probe, args[p])
		}
	}
	if len(want) == 0 || sameClaudeMeaning(want, ReadClaudeArgs(probe).Options) {
		return []string{literal}
	}
	var encoded []string
	for _, o := range want {
		if len(o.Values) > 0 && len(o.ValueIndices) == 0 {
			// Long equals syntax preserves even dashed values and a leading
			// equals in the original short suffix (e.g. -d=2e).
			encoded = append(encoded, o.Canonical+"="+o.Values[0])
		} else {
			encoded = append(encoded, o.Name)
		}
	}
	return encoded
}
