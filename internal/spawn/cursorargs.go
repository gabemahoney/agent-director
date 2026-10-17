package spawn

import "strings"

// CursorArgsReading retains original token ownership for validation and selector
// removal. It does no I/O and does not imply that a recognized option is allowed.
type CursorArgsReading struct {
	Options     []ArgOption
	Positionals []int
	DoubleDash  int
}

// ReadCursorArgs shares the verified model/mode and selector reading. Model and
// mode syntax is attributed to 2026.10.12-d47ca18; selector syntax to the
// 2026.09.28-64d2043 command definition. Required values own dashed tokens too.
// Unknown options are valueless and remain subject to fail-closed validation.
func ReadCursorArgs(args []string) CursorArgsReading {
	out := CursorArgsReading{DoubleDash: -1}
	command := true
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			out.DoubleDash = i
			for p := i + 1; p < len(args); p++ {
				out.Positionals = append(out.Positionals, p)
			}
			break
		}
		key, value, equals := strings.Cut(token, "=")
		if !strings.HasPrefix(token, "-") && !(command && token == "resume") {
			out.Positionals = append(out.Positionals, i)
			command = false
			continue
		}
		o := ArgOption{Index: i, Name: key, Canonical: key, Spelling: token}
		if key == "-m" {
			o.Canonical = "--model"
		}
		required := key == "--model" || key == "-m" || key == "--mode" || key == "--new-session-id" || key == "--sandbox" || key == "--api-key" || key == "--header" || key == "--exclude-tools"
		optional := key == "--resume"
		o.Known = required || optional || key == "--continue" || key == "resume" || key == "--plan" || key == "--force" || key == "--yolo" || key == "--trust"
		if equals {
			o.Values = []string{value}
		} else if i+1 < len(args) && (required || (optional && !strings.HasPrefix(args[i+1], "-"))) {
			i++
			o.Values, o.ValueIndices = []string{args[i]}, []int{i}
		}
		if key == "resume" {
			command = false
		}
		out.Options = append(out.Options, o)
	}
	return out
}
