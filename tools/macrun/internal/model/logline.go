package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// LogMarker starts every marked log line: the marker, a space, then one
// compact JSON Event. GitHub's runner gives it no meaning, as it does lines
// starting :: (workflow commands) or ##[ (log directives).
const LogMarker = "@macrun-event"

// maxLogLineBytes is the longest log line ReadLog reads. A longer line is
// skipped: marked lines are far shorter, and test output can be anything.
const maxLogLineBytes = 1 << 20

// EventKind names a marked log line's event.
type EventKind string

// The events a part writes to its job log, in the order it writes them.
const (
	// EventPartStart: the part's identity, versions and configurations,
	// before its first package.
	EventPartStart EventKind = "part-start"
	// EventSetupFailed: a check or setup step stopped the part before any
	// case. It carries the part's identity too, since no part-start precedes
	// it.
	EventSetupFailed EventKind = "setup-failed"
	// EventPackageBuild: one package's test binary built, or didn't.
	EventPackageBuild EventKind = "package-build"
	// EventCaseStart: a case started.
	EventCaseStart EventKind = "case-start"
	// EventCaseEnd: a case ended, with its result but not its output.
	EventCaseEnd EventKind = "case-end"
	// EventPartEnd: how the part ended.
	EventPartEnd EventKind = "part-end"
)

// Event is one marked log line's JSON object. Each kind sets only its own
// fields; the rest are left out of the line.
type Event struct {
	// Event is the event's kind.
	Event EventKind `json:"event"`

	// Part, RunID, Attempt and Commit identify the part, on part-start and
	// setup-failed.
	Part    string `json:"part,omitempty"`
	RunID   int64  `json:"run_id,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// Versions and Configurations are set on part-start.
	Versions       []Version       `json:"versions,omitempty"`
	Configurations []Configuration `json:"configurations,omitempty"`

	// Step and Message are set on setup-failed.
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`

	// Dir and Built are set on package-build.
	Dir   string `json:"dir,omitempty"`
	Built bool   `json:"built,omitempty"`

	// Case is set on case-start and case-end; Result, Reason and Elapsed on
	// case-end.
	Case    CaseID  `json:"case,omitempty"`
	Result  Result  `json:"result,omitempty"`
	Reason  Reason  `json:"reason,omitempty"`
	Elapsed float64 `json:"elapsed,omitempty"`

	// End is set on part-end.
	End PartEnd `json:"end,omitempty"`
}

// WriteEvent writes e to w as one marked log line.
func WriteEvent(w io.Writer, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode %s line: %w", e.Event, err)
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", LogMarker, data); err != nil {
		return fmt.Errorf("write %s line: %w", e.Event, err)
	}
	return nil
}

// byteOrderMark is UTF-8's byte-order mark, which GitHub's log download can
// start with.
const byteOrderMark = "\xef\xbb\xbf"

// ReadLog rebuilds a part's record from its GitHub job log: lines that each
// start with a timestamp and a space, which it strips. It reads only the
// lines whose content starts with LogMarker, tolerating a byte-order mark,
// \r\n line ends and lines longer than it reads, which it skips. The record
// has no output and no expected cases, and says it came from a log; a case
// that started and never ended has no result. ok is false when the log has
// no marked line. A marked line whose JSON doesn't parse, or whose event is
// unknown, is an error naming its line.
func ReadLog(r io.Reader) (rec Record, ok bool, err error) {
	b := logBuilder{
		rec:   Record{SchemaVersion: RecordSchemaVersion, FromLog: true},
		ended: map[CaseID]int{},
		open:  map[CaseID]int{},
	}
	br := bufio.NewReaderSize(r, maxLogLineBytes)
	for number := 1; ; number++ {
		line, readErr := readLogLine(br)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return Record{}, false, fmt.Errorf("read job log line %d: %w", number, readErr)
		}
		if payload, marked := markedPayload(line, number == 1); marked {
			var e Event
			if err := json.Unmarshal([]byte(payload), &e); err != nil {
				return Record{}, false, fmt.Errorf("job log line %d: marked line doesn't parse: %w", number, err)
			}
			if err := b.add(e); err != nil {
				return Record{}, false, fmt.Errorf("job log line %d: %w", number, err)
			}
			ok = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return b.finish(), ok, nil
}

// readLogLine reads one line, without its line end. A line longer than the
// reader's buffer is read to its end and returned empty.
func readLogLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return string(bytes.TrimRight(line, "\r\n")), err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = br.ReadSlice('\n')
	}
	return "", err
}

// markedPayload strips a log line's timestamp and returns the JSON after the
// marker, if its content starts with the marker.
func markedPayload(line string, first bool) (string, bool) {
	if first {
		line = strings.TrimPrefix(line, byteOrderMark)
	}
	if stamp, content, found := strings.Cut(line, " "); found {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			line = content
		}
	}
	payload, found := strings.CutPrefix(line, LogMarker+" ")
	return payload, found
}

// logBuilder rebuilds a record from its events.
type logBuilder struct {
	rec Record
	// ended maps each ended case to its index in rec.Cases.
	ended map[CaseID]int
	// open counts each case's starts that have not ended yet.
	open map[CaseID]int
	// started lists the cases in the order they first started.
	started []CaseID
}

func (b *logBuilder) add(e Event) error {
	switch e.Event {
	case EventPartStart:
		b.identify(e)
		b.rec.Versions = e.Versions
		b.rec.Configurations = e.Configurations
	case EventSetupFailed:
		b.identify(e)
		b.rec.SetupFailure = &SetupFailure{Step: e.Step, Message: e.Message}
	case EventPackageBuild:
		b.rec.Packages = append(b.rec.Packages, PackageBuild{Dir: e.Dir, Built: e.Built})
	case EventCaseStart:
		if _, seen := b.open[e.Case]; !seen {
			b.started = append(b.started, e.Case)
		}
		b.open[e.Case]++
	case EventCaseEnd:
		if b.open[e.Case] > 0 {
			b.open[e.Case]--
		}
		c := CaseRecord{CaseResult: CaseResult{Case: e.Case, Result: e.Result, Reason: e.Reason, Elapsed: e.Elapsed}}
		if i, seen := b.ended[e.Case]; seen {
			b.rec.Cases[i] = MergeCase(b.rec.Cases[i], c)
		} else {
			b.ended[e.Case] = len(b.rec.Cases)
			b.rec.Cases = append(b.rec.Cases, c)
		}
	case EventPartEnd:
		b.rec.End = e.End
	default:
		return fmt.Errorf("marked line has unknown event %q", e.Event)
	}
	return nil
}

func (b *logBuilder) identify(e Event) {
	b.rec.Part, b.rec.RunID, b.rec.Attempt, b.rec.Commit = e.Part, e.RunID, e.Attempt, e.Commit
}

// finish lists the cases still open as without a result.
func (b *logBuilder) finish() Record {
	for _, id := range b.started {
		if b.open[id] > 0 {
			b.rec.WithoutResult = append(b.rec.WithoutResult, id)
		}
	}
	return b.rec
}
