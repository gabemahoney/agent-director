package main

import (
	"strconv"
	"strings"
)

// configCommands decodes only quoteConfigArg's grammar (b.xhh), not arbitrary
// tmux configuration. Input is read with ReadAll, without a line-length cap.
// Decoded arguments bypass splitCommands: a quoted final semicolon is literal.
func configCommands(input string) ([][]string, error) {
	if !strings.HasSuffix(input, "\n") {
		return nil, errBadArgv
	}
	input = strings.TrimSuffix(input, "\n")
	cmds := [][]string{{}}
	for i := 0; i < len(input); {
		if input[i] != '\'' {
			return nil, errBadArgv
		}
		var arg strings.Builder
		for {
			i++ // opening quote
			for i < len(input) && input[i] != '\'' {
				if input[i] < 0x20 || input[i] == 0x7f {
					return nil, errBadArgv
				}
				arg.WriteByte(input[i])
				i++
			}
			if i == len(input) {
				return nil, errBadArgv
			}
			i++ // closing quote
			if i == len(input) || input[i] != '\\' {
				break
			}
			if i+4 >= len(input) || input[i+4] != '\'' {
				return nil, errBadArgv
			}
			b, err := strconv.ParseUint(input[i+1:i+4], 8, 8)
			if err != nil || b == 0 || (b != '\'' && b >= 0x20 && b != 0x7f) {
				return nil, errBadArgv
			}
			arg.WriteByte(byte(b))
			i += 4 // next opening quote
		}
		cmds[len(cmds)-1] = append(cmds[len(cmds)-1], arg.String())
		if i == len(input) {
			break
		}
		if strings.HasPrefix(input[i:], " ; ") {
			cmds = append(cmds, []string{})
			i += 3
		} else if input[i] == ' ' {
			i++
		} else {
			return nil, errBadArgv
		}
		if i == len(input) {
			return nil, errBadArgv
		}
	}
	for i, cmd := range cmds {
		if len(cmd) == 0 || (i == 0 && cmd[0] != "new-session") || (i > 0 && cmd[0] != "set-option") {
			return nil, errBadArgv
		}
	}
	return cmds, nil
}
