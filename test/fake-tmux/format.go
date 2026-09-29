package main

import (
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

// vars is the format context of one line: the values of the #{...}
// variables the client's -F formats name.
type vars map[string]string

// expand expands a tmux -F format with v: "##" is "#", "#{name}" is v's
// value (empty for a name v lacks, as tmux gives), and any other "#" stays.
// Nested formats and conditionals are not supported; the client sends none.
func expand(format string, v vars) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '#' || i+1 == len(format) {
			b.WriteByte(c)
			continue
		}
		switch format[i+1] {
		case '#':
			b.WriteByte('#')
			i++
		case '{':
			end := strings.IndexByte(format[i+2:], '}')
			if end < 0 {
				b.WriteString(format[i:])
				return b.String()
			}
			b.WriteString(v[format[i+2:i+2+end]])
			i += 2 + end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sessionVars is the context of session s on the table's server, its
// active pane being its first.
func sessionVars(tb *faketmuxfix.Table, s *faketmuxfix.Session) vars {
	v := vars{
		"session_id":      s.ID,
		"session_created": strconv.FormatInt(s.Created, 10),
		"session_name":    s.Name,
		"@ad_owner":       ownerValue(tb, s),
	}
	if tb.Server != nil {
		v["pid"] = strconv.Itoa(tb.Server.PID)
		v["start_time"] = strconv.FormatInt(tb.Server.Start, 10)
	}
	if len(s.Panes) > 0 {
		for k, val := range paneVars(s.Panes[0]) {
			v[k] = val
		}
	}
	return v
}

// paneVars is the pane part of a format context.
func paneVars(p faketmuxfix.Pane) vars {
	return vars{
		"window_index": strconv.Itoa(p.Window),
		"pane_index":   strconv.Itoa(p.Index),
		"pane_id":      p.ID,
		"pane_pid":     strconv.Itoa(p.PID),
	}
}

// ownerValue is #{@ad_owner} for session s: a server or global-window value
// first, then the session's own label, then the global value (tmux's
// format lookup order for a user option; provenance-fresh F2, F9b).
func ownerValue(tb *faketmuxfix.Table, s *faketmuxfix.Session) string {
	switch {
	case tb.Scope.Server != "":
		return tb.Scope.Server
	case tb.Scope.GlobalWindow != "":
		return tb.Scope.GlobalWindow
	case s.Label != "":
		return s.Label
	}
	return tb.Scope.Global
}
