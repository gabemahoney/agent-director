//go:build linux

package cursorhost

import (
	"bufio"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
)

// terminalInputLoop owns UTF-8 input decoding. Literal LF adds a line; Enter
// (CR) submits one prompt. tmux send-keys sends embedded LF literally and
// then a separate Enter, so consecutive multiline messages remain distinct.
func terminalInputLoop(in *os.File, out io.Writer, events chan<- terminalInput, admit func() bool, choices ...func(rune) (string, string, bool)) func() {
	return terminalLoopWithStop(in, out, func(v terminalInput, stop <-chan struct{}) {
		select {
		case events <- v:
		case <-stop:
		}
	}, admit, choices...)
}

func terminalLoop(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
	return terminalLoopWithStop(in, out, func(v terminalInput, _ <-chan struct{}) { emit(v) }, admit, choices...)
}

// The session's mailbox, admission and choice callbacks are already bounded.
// A blocking channel adapter instead receives stop so it can release delivery.
// Cleanup joins this reader before restoring or returning the borrowed handles.
func terminalLoopWithStop(in *os.File, out io.Writer, deliver func(terminalInput, <-chan struct{}), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
	return terminalLoopWithReader(in, out, deliver, admit, nil, choices...)
}

// configure observes or wraps this reader's I/O before its decoder starts.
func terminalLoopWithReader(in *os.File, out io.Writer, deliver func(terminalInput, <-chan struct{}), admit func() bool, configure func(*terminalReader), choices ...func(rune) (string, string, bool)) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	input, old, err := prepareTerminal(in, stop)
	if err == nil && configure != nil {
		configure(input)
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			close(stop)
			if input != nil {
				// Closing our pipe writer wakes even a poll not yet entered.
				input.wakeWrite.Close()
			}
			<-done
			if input != nil {
				fmt.Fprint(out, "\x1b[?2004l")
				unix.IoctlSetTermios(input.fd, unix.TCSETS, old)
				input.wakeRead.Close()
				runtime.KeepAlive(input.in)
			}
		})
	}
	emit := func(v terminalInput) {
		if !terminalStopped(stop) {
			deliver(v, stop)
		}
	}
	if err == nil {
		fmt.Fprint(out, "\x1b[?2004h")
	}
	go func() {
		defer close(done)
		if err != nil {
			// Even a setup failure must return cleanup before a channel adapter
			// can block delivering it.
			emit(terminalInput{err: err})
			return
		}
		reader := bufio.NewReader(input)
		var text []rune
		cursor := 0
		paste := false
		redraw := func() {
			if terminalStopped(stop) {
				return
			}
			fmt.Fprint(out, "\r\x1b[2K> "+strings.ReplaceAll(string(text), "\n", " ↵ "))
			if tail := len(text) - cursor; tail > 0 && !terminalStopped(stop) {
				fmt.Fprintf(out, "\x1b[%dD", tail)
			}
		}
		for !terminalStopped(stop) {
			r, size, e := reader.ReadRune()
			if terminalStopped(stop) {
				return
			}
			if e != nil {
				emit(terminalInput{err: e})
				return
			}
			if r == utf8.RuneError && size == 1 {
				emit(terminalInput{err: fmt.Errorf("invalid UTF-8 input")})
				return
			}
			if !paste && len(text) == 0 && len(choices) > 0 && r >= ' ' {
				if token, option, ok := choices[0](r); ok {
					emit(terminalInput{choiceToken: token, choiceOption: option})
					continue
				}
			}
			if terminalStopped(stop) {
				return
			}
			if r == '\x1b' {
				// Only bounded known input escape sequences are interpreted, never
				// output/screen text. Bracketed paste preserves newline boundaries.
				seq := ""
				for len(seq) < 8 {
					if terminalStopped(stop) {
						return
					}
					if reader.Buffered() == 0 {
						ready, e := input.wait(80)
						if e != nil {
							emit(terminalInput{err: fmt.Errorf("Cursor pane input failed")})
							return
						}
						if !ready {
							break
						}
					}
					ch, e := reader.ReadByte()
					if e != nil {
						emit(terminalInput{err: e})
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
					emit(terminalInput{interrupt: true})
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
				emit(terminalInput{err: fmt.Errorf("Cursor input exceeds limit")})
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
				if terminalStopped(stop) {
					return
				}
				if submitted != "" && submitted != "/exit" && submitted != "/new" && submitted != "/clear" {
					accepted = admit()
				}
				emit(terminalInput{text: submitted, accepted: accepted})
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
				emit(terminalInput{interrupt: true})
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
						emit(terminalInput{err: fmt.Errorf("Cursor input exceeds limit")})
						return
					}
					text = append(text[:cursor], append([]rune{r}, text[cursor:]...)...)
					cursor++
					redraw()
				}
			}
		}
	}()
	return cleanup
}

// Only the decoder reads the borrowed descriptor until cleanup returns. With
// raw VMIN=1, readiness permits a read without waiting for any further bytes.
// The private pipe interrupts both idle reads and partial UTF-8/Escape input;
// neither closing nor changing the borrowed descriptor's flags is necessary.
type terminalReader struct {
	in                  *os.File
	fd                  int
	wakeRead, wakeWrite *os.File
	stop                <-chan struct{}
	poll                func([]unix.PollFd, int) (int, error)
	read                func(int, []byte) (int, error)
}

func prepareTerminal(in *os.File, stop <-chan struct{}) (*terminalReader, *unix.Termios, error) {
	old, err := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
	if err != nil {
		return nil, nil, fmt.Errorf("Cursor gateway requires an interactive pane")
	}
	wakeRead, wakeWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("Cursor pane input unavailable")
	}
	raw := *old
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(int(in.Fd()), unix.TCSETS, &raw); err != nil {
		wakeRead.Close()
		wakeWrite.Close()
		return nil, nil, fmt.Errorf("Cursor pane input unavailable")
	}
	return &terminalReader{in: in, fd: int(in.Fd()), wakeRead: wakeRead, wakeWrite: wakeWrite, stop: stop, poll: unix.Poll, read: unix.Read}, old, nil
}

func terminalStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// A negative timeout is an idle/UTF-8 wait with no timer. Only the existing
// Escape parser supplies a finite timeout, separately for each continuation.
func (r *terminalReader) wait(timeout int) (bool, error) {
	if terminalStopped(r.stop) {
		return false, os.ErrClosed
	}
	poll := []unix.PollFd{
		{Fd: int32(r.fd), Events: unix.POLLIN},
		{Fd: int32(r.wakeRead.Fd()), Events: unix.POLLIN},
	}
	n, err := r.poll(poll, timeout)
	runtime.KeepAlive(r.in)
	if terminalStopped(r.stop) {
		return false, os.ErrClosed
	}
	if err != nil {
		return false, err
	}
	if poll[0].Revents&unix.POLLNVAL != 0 {
		return false, os.ErrClosed
	}
	return n > 0, nil
}

func (r *terminalReader) Read(p []byte) (int, error) {
	for {
		_, err := r.wait(-1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		n, err := r.read(r.fd, p)
		runtime.KeepAlive(r.in)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

const maxInputRunes = 256 * 1024
