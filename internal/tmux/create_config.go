package tmux

import (
	"fmt"
	"strings"
)

// quoteConfigArg renders one argument for tmux's configuration parser, not a
// shell or its argv parser. Single quotes prevent variable/tilde expansion.
// Quotes and control bytes use octal escapes outside those quotes; literal
// newlines would let tmux strip following whitespace/comments. Every argument
// in a source-file create must use this function.
func quoteConfigArg(value string) (string, bool) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", false
	}
	var out strings.Builder
	out.WriteByte('\'')
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b == '\'' || b < 0x20 || b == 0x7f {
			fmt.Fprintf(&out, "'\\%03o'", b)
		} else {
			out.WriteByte(b)
		}
	}
	out.WriteByte('\'')
	return out.String(), true
}

// createConfig keeps the entire create/label group on one logical line, so a
// failing command stops its remaining labels as it does in the argv group.
func createConfig(commands [][]string) ([]byte, error) {
	var out strings.Builder
	for i, command := range commands {
		if i > 0 {
			out.WriteString(" ; ")
		}
		for j, arg := range command {
			quoted, ok := quoteConfigArg(arg)
			if !ok {
				return nil, createRefusal(fmt.Sprintf("create command %d argument %d contains an unrepresentable byte", i, j))
			}
			if j > 0 {
				out.WriteByte(' ')
			}
			out.WriteString(quoted)
		}
	}
	out.WriteByte('\n')
	return []byte(out.String()), nil
}

func createRefusal(message string) *CallError {
	return &CallError{Call: CallCreate, Failure: FailUnrecognized, BeforeCreate: true, ExitStatus: -1, FirstLine: message}
}

const firstLineWithheld = "create diagnostic withheld"

// privateCreateResult retains only a valid create reply and recognized error
// forms. It preserves output presence for uncertain-create classification,
// without allowing tmux parser errors to disclose any part of stdin.
func privateCreateResult(res RunResult, socket string) RunResult {
	if _, ok := parseCreateReply(res.Stdout); !ok && len(res.Stdout) > 0 {
		res.Stdout = []byte(firstLineWithheld)
		// source-file can write parser diagnostics only to stdout. Give
		// runFailure the marker on stderr, where it reads non-zero exits.
		if len(res.Stderr) == 0 {
			res.Stderr = []byte(firstLineWithheld)
		}
	}
	if len(res.Stderr) > 0 {
		line := trimmedFirstLine(res.Stderr)
		line = strings.TrimPrefix(line, "<stdin>:1: ")
		failure, foundSocket, _ := recognizeReply(CallCreate, []byte(line))
		switch {
		case failure == FailDuplicate:
			res.Stderr = []byte(replyDuplicatePrefix + "[withheld]")
		case (failure == FailNoServer || failure == FailNoSocket || failure == FailSocketDenied) && foundSocket == socket:
			res.Stderr = []byte(line)
		default:
			res.Stderr = []byte(firstLineWithheld)
		}
	}
	return res
}
