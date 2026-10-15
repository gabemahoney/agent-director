//go:build linux

package cursorhost

import (
	"bufio"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

type terminalInput struct {
	choiceToken  string
	choiceOption string
	accepted     bool
	text         string
	interrupt    bool
	err          error
}

// terminalInputLoop owns UTF-8 input decoding. Literal LF adds a line; Enter
// (CR) submits one prompt. tmux send-keys sends embedded LF literally and
// then a separate Enter, so consecutive multiline messages remain distinct.
func terminalInputLoop(in *os.File, out io.Writer, events chan<- terminalInput, admit func() bool, choices ...func(rune) (string, string, bool)) func() {
	old, err := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
	if err != nil {
		events <- terminalInput{err: fmt.Errorf("Cursor gateway requires an interactive pane")}
		return func() {}
	}
	raw := *old
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(int(in.Fd()), unix.TCSETS, &raw); err != nil {
		events <- terminalInput{err: fmt.Errorf("Cursor pane input unavailable")}
		return func() {}
	}
	fmt.Fprint(out, "\x1b[?2004h")
	go func() {
		reader := bufio.NewReader(in)
		var text []rune
		cursor := 0
		paste := false
		redraw := func() {
			fmt.Fprint(out, "\r\x1b[2K> "+strings.ReplaceAll(string(text), "\n", " ↵ "))
			if tail := len(text) - cursor; tail > 0 {
				fmt.Fprintf(out, "\x1b[%dD", tail)
			}
		}
		for {
			r, size, e := reader.ReadRune()
			if e != nil {
				events <- terminalInput{err: e}
				return
			}
			if r == utf8.RuneError && size == 1 {
				events <- terminalInput{err: fmt.Errorf("invalid UTF-8 input")}
				return
			}
			if !paste && len(text) == 0 && len(choices) > 0 && r >= ' ' {
				if token, option, ok := choices[0](r); ok {
					events <- terminalInput{choiceToken: token, choiceOption: option}
					continue
				}
			}
			if r == '\x1b' {
				// Only bounded known input escape sequences are interpreted, never
				// output/screen text. Bracketed paste preserves newline boundaries.
				seq := ""
				for len(seq) < 8 {
					if reader.Buffered() == 0 {
						poll := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}}
						n, e := unix.Poll(poll, 80)
						if e != nil {
							events <- terminalInput{err: fmt.Errorf("Cursor pane input failed")}
							return
						}
						if n == 0 {
							break
						}
					}
					ch, e := reader.ReadByte()
					if e != nil {
						events <- terminalInput{err: e}
						return
					}
					seq += string(ch)
					if ch == '~' || (ch >= 'A' && ch <= 'Z') {
						break
					}
				}
				switch seq {
				case "":
					text = nil
					cursor = 0
					events <- terminalInput{interrupt: true}
				case "[200~":
					paste = true
				case "[201~":
					paste = false
				case "[D":
					if cursor > 0 {
						cursor--
					}
				case "[C":
					if cursor < len(text) {
						cursor++
					}
				case "[H":
					cursor = 0
				case "[F":
					cursor = len(text)
				case "[3~":
					if cursor < len(text) {
						text = append(text[:cursor], text[cursor+1:]...)
					}
				}
				redraw()
				continue
			}
			if len(text) >= maxInputRunes && (r == '\n' || (r == '\r' && paste)) {
				events <- terminalInput{err: fmt.Errorf("Cursor input exceeds limit")}
				return
			}
			switch r {
			case '\r':
				if paste {
					text = append(text[:cursor], append([]rune{'\n'}, text[cursor:]...)...)
					cursor++
					redraw()
					continue
				}
				submitted := string(text)
				text = nil
				cursor = 0
				fmt.Fprint(out, "\r\n")
				accepted := false
				if submitted != "" && submitted != "/exit" && submitted != "/new" && submitted != "/clear" {
					accepted = admit()
				}
				events <- terminalInput{text: submitted, accepted: accepted}
			case '\n':
				text = append(text[:cursor], append([]rune{'\n'}, text[cursor:]...)...)
				cursor++
				redraw()
			case '\x15':
				text = nil
				cursor = 0
				redraw() // pause's real C-u sequence clears the whole draft
			case '\x03':
				text = nil
				cursor = 0
				fmt.Fprint(out, "\r\n")
				events <- terminalInput{interrupt: true}
			case '\x7f', '\b':
				if cursor > 0 {
					text = append(text[:cursor-1], text[cursor:]...)
					cursor--
					redraw()
				}
			case '\x01':
				cursor = 0
				redraw()
			case '\x05':
				cursor = len(text)
				redraw()
			default:
				if r >= ' ' || r == '\t' {
					if len(text) >= maxInputRunes {
						events <- terminalInput{err: fmt.Errorf("Cursor input exceeds limit")}
						return
					}
					text = append(text[:cursor], append([]rune{r}, text[cursor:]...)...)
					cursor++
					redraw()
				}
			}
		}
	}()
	return func() { fmt.Fprint(out, "\x1b[?2004l"); unix.IoctlSetTermios(int(in.Fd()), unix.TCSETS, old) }
}

const maxInputRunes = 256 * 1024
