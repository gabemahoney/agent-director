package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Evidence an agent team scenario keeps when it does not get its answer
// (Gabe, 2026-10-02: the split-pane lead of L2 accepted no input and left
// nothing that said why). Three harness-only aids, none a production change:
//
//   - the input-ready wait: the team prompt is sent only once the lead's
//     pane shows its prompt box (inputReady), bounded by -input-ready-timeout;
//     a lead that never gets there is recorded as "input never ready";
//   - pane captures: when a team scenario is cut short (input never ready,
//     the prompt not accepted, the team not settled), the text of every pane
//     of the lead's tmux session, and of any pane on a separate claude-swarm
//     server under the private socket directory, is written to the results
//     directory before pause, and again after a pause that fails;
//   - Claude's debug log: the lead runs with --debug-file under the run's
//     HOME, and after the scenario that file and any debug log Claude wrote
//     under $HOME/.claude/debug during it are copied into the results
//     directory, so they survive the container's --rm.
//
// These files, and the pane excerpt an error or reason quotes, are Claude's
// own screen and log text, passed through scrubEvidence first: the exact
// credential values, the gateway URL's host and longer path parts and every
// 12-character piece of a credential are replaced, and a line naming a
// credential is withheld. That removes what the driver knows to look for;
// it cannot prove that no other secret text Claude shows or logs is gone,
// which is why a live run's command file also checks every result file
// before printing anything. The input-ready check reads the pane text with
// only the exact values replaced. Captures and copies are read-only
// towards Claude and tmux (capture-pane and list-panes on the private
// server, logged as reads).

// inputReadyGrace is the pause between seeing the prompt box and sending
// the team prompt, so a TUI that has just drawn its input does not get the
// keystrokes against stale state (candidate (b) of the split-pane finding:
// 2.1.283 fixed keys typed quickly together being handled against stale
// state).
const inputReadyGrace = 2 * time.Second

// errInputNeverReady is a lead whose prompt box never showed within
// -input-ready-timeout. The team prompt is then not sent.
var errInputNeverReady = errors.New("input never ready")

// paneExcerptLines and paneExcerptMax bound the pane excerpt an error or a
// scenario reason quotes (the whole text is in the capture file).
const (
	paneExcerptLines = 6
	paneExcerptMax   = 400
)

// maxDebugCopyBytes bounds one copied debug log; a longer one keeps its
// tail (the end of a stalled session is what explains it).
const maxDebugCopyBytes = 32 << 20

// swarmSocketGlob matches the separate tmux servers Claude Code starts for
// split-pane teammates when it is not inside tmux ("external session
// mode": tmux -L claude-swarm-<pid>, under the same TMUX_TMPDIR).
const swarmSocketGlob = "claude-swarm-*"

var (
	// promptLineRE is a line of the input prompt: an optional box edge,
	// then Claude Code's prompt marker (❯, or > in older versions) followed
	// by a space or the end of the line.
	promptLineRE = regexp.MustCompile(`^[\s│|]*(❯|>)([\s\x{00a0}]|$)`)
	// dialogOptionRE is a selection cursor on a numbered option: a
	// selection dialog (trust, theme, a confirmation) holds the screen.
	dialogOptionRE = regexp.MustCompile(`^[\s│|]*❯[\s\x{00a0}]*\d+\.`)
)

// dialogMarkers are footers only a dialog shows.
var dialogMarkers = []string{"Enter to confirm", "Esc to cancel", "Esc to exit", "Press Enter to continue"}

// shortcutsHint is the footer Claude Code shows under an empty input.
const shortcutsHint = "? for shortcuts"

// inputReady reads a pane's text: ready when no dialog holds the screen and
// the input prompt shows (a prompt line or the shortcuts hint). why names
// what was seen, for the scenario notes.
func inputReady(text string) (ready bool, why string) {
	prompt, hint := false, strings.Contains(text, shortcutsHint)
	for _, line := range strings.Split(text, "\n") {
		if dialogOptionRE.MatchString(line) {
			return false, "a selection dialog is showing"
		}
		if promptLineRE.MatchString(line) {
			prompt = true
		}
	}
	for _, m := range dialogMarkers {
		if strings.Contains(text, m) {
			return false, fmt.Sprintf("a dialog is showing (%q)", m)
		}
	}
	switch {
	case prompt && hint:
		return true, "prompt line and \"" + shortcutsHint + "\" seen"
	case prompt:
		return true, "prompt line seen"
	case hint:
		return true, "\"" + shortcutsHint + "\" seen"
	}
	return false, "no prompt line"
}

// paneExcerpt is the last non-empty lines of a pane's text on one line,
// bounded, for an error or a reason.
func paneExcerpt(text string) string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "(blank)"
	}
	if len(lines) > paneExcerptLines {
		lines = lines[len(lines)-paneExcerptLines:]
	}
	s := strings.Join(lines, " | ")
	if r := []rune(s); len(r) > paneExcerptMax {
		s = "…" + string(r[len(r)-paneExcerptMax:])
	}
	return s
}

// paneText reads one pane's text on socket with only the exact credential
// values replaced (scrub; whatever of it is written to the results goes
// through scrubEvidence as well): wrapped lines joined, no escape
// sequences; with history, its whole history and screen, else the visible
// screen only (the input-ready check reads what shows now, not a dialog
// long scrolled away).
func (h *harness) paneText(caseID, socket, paneID string, history bool) (string, error) {
	argv := []string{"capture-pane", "-p", "-J"}
	if history {
		argv = append(argv, "-S", "-")
	}
	res, err := h.inv.tmuxCall(caseID, actionRead, socket, append(argv, "-t", paneID)...)
	if err != nil {
		return "", err
	}
	return h.scr.scrub(string(res.stdout)), nil
}

// waitInputReady polls the agent's pane text until its input prompt shows
// (inputReady), then waits inputReadyGrace. It gives up with
// errInputNeverReady, quoting the last text seen, after
// -input-ready-timeout. Permission requests are answered meanwhile.
func (d *rn9Agent) waitInputReady() error {
	h := d.h
	start := h.clock.Now()
	deadline := start.Add(h.cfg.inputReadyTimeout)
	last, lastWhy, lastErr := "", "", error(nil)
	for {
		text, err := h.paneText(d.caseID, d.a.Socket, d.a.PaneID, false)
		lastErr = err
		if err == nil {
			last = text
			ready, why := inputReady(text)
			lastWhy = why
			if ready {
				d.notes = append(d.notes, fmt.Sprintf("input ready after %s (%s); team prompt sent %s later",
					h.clock.Now().Sub(start).Round(100*time.Millisecond), why, inputReadyGrace))
				h.clock.Sleep(inputReadyGrace)
				return nil
			}
		}
		if err := d.answerPermissions(); err != nil {
			return err
		}
		if !h.clock.Now().Before(deadline) {
			detail := fmt.Sprintf("the lead's prompt box was not seen within %s", h.cfg.inputReadyTimeout)
			if lastWhy != "" {
				detail += " (" + lastWhy + ")"
			}
			if lastErr != nil {
				detail += "; last capture failed: " + lastErr.Error()
			}
			return fmt.Errorf("%w: %s; the team prompt was not sent; pane shows: %s", errInputNeverReady, detail, paneExcerpt(h.scr.scrubEvidence(last)))
		}
		h.clock.Sleep(rn9Poll)
	}
}

// capturedPane is one written pane capture.
type capturedPane struct {
	File string // file name in the results directory
	Text string
}

// capturePanes writes the text of every pane of the agent's tmux session,
// and of every pane on a claude-swarm server under the private socket
// directory, to <case>-pane-<label><suffix>.txt in the results directory,
// after scrubEvidence. The agent's own pane is labelled "lead". It returns the captures written
// and notes for what could not be read; it never fails the scenario.
func (h *harness) capturePanes(caseID string, a agentRef, suffix string) ([]capturedPane, []string) {
	var (
		out   []capturedPane
		notes []string
	)
	capture := func(socket, paneID, label string) {
		text, err := h.paneText(caseID, socket, paneID, true)
		if err != nil {
			notes = append(notes, fmt.Sprintf("pane %s (%s): capture failed: %v", paneID, label, err))
			return
		}
		text = h.scr.scrubEvidence(text)
		name := fmt.Sprintf("%s-pane-%s%s.txt", caseID, label, suffix)
		if err := os.WriteFile(filepath.Join(h.cfg.outDir, name), []byte(text), 0o600); err != nil {
			notes = append(notes, fmt.Sprintf("pane %s (%s): not written: %v", paneID, label, err))
			return
		}
		out = append(out, capturedPane{File: name, Text: text})
	}
	if a.PaneID != "" {
		capture(a.Socket, a.PaneID, "lead")
	}
	if a.TmuxSessionID != "" {
		res, err := h.inv.tmuxCall(caseID, actionRead, a.Socket, "list-panes", "-s", "-t", a.TmuxSessionID, "-F", "#{pane_id}")
		if err != nil {
			notes = append(notes, "panes of the lead's session not listed: "+err.Error())
		} else {
			for _, p := range strings.Fields(string(res.stdout)) {
				if p != a.PaneID {
					capture(a.Socket, p, "teammate-"+paneLabel(p))
				}
			}
		}
	}
	swarms, _ := filepath.Glob(filepath.Join(filepath.Dir(a.Socket), swarmSocketGlob))
	sort.Strings(swarms)
	for _, s := range swarms {
		res, err := h.inv.tmuxCall(caseID, actionRead, s, "list-panes", "-a", "-F", "#{pane_id}")
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: panes not listed: %v", filepath.Base(s), err))
			continue
		}
		for _, p := range strings.Fields(string(res.stdout)) {
			capture(s, p, filepath.Base(s)+"-"+paneLabel(p))
		}
	}
	return out, notes
}

// paneLabel makes a tmux pane id ("%3") safe in a file name ("p3").
func paneLabel(paneID string) string {
	return "p" + strings.TrimLeft(paneID, "%")
}

// leadDebugFile is where an agent team lead writes Claude's debug log
// (--debug-file): under the run's HOME, never the results directory, so
// only the copy keepDebugLogs scrubs is ever kept.
func (h *harness) leadDebugFile(caseID string) (string, error) {
	dir := filepath.Join(h.iso.Home, "mx-claude-debug")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, caseID+"-lead.txt"), nil
}

// claudeDebugDir is where Claude Code writes its debug logs by default
// ($HOME/.claude/debug/<session id>.txt; CLAUDE_CONFIG_DIR never reaches an
// agent).
func (h *harness) claudeDebugDir() string {
	return filepath.Join(h.iso.Home, ".claude", "debug")
}

// fileStamp is a file's size and modification time.
type fileStamp struct {
	size int64
	mod  time.Time
}

// debugLogStamps lists the regular files in Claude's default debug
// directory, so a scenario copies only the logs written during it.
func (h *harness) debugLogStamps() map[string]fileStamp {
	m := map[string]fileStamp{}
	entries, _ := os.ReadDir(h.claudeDebugDir())
	for _, e := range entries {
		fi, err := e.Info()
		if err == nil && fi.Mode().IsRegular() {
			m[e.Name()] = fileStamp{fi.Size(), fi.ModTime()}
		}
	}
	return m
}

// keepDebugLogs copies, through scrubEvidence, the lead's --debug-file log
// and every debug log Claude wrote or changed in its default directory
// since before (a debugLogStamps listing) into the results directory as
// <case>-claude-debug-<name>. It returns the files written and notes.
func (h *harness) keepDebugLogs(caseID, leadFile string, before map[string]fileStamp) ([]string, []string) {
	var (
		files []string
		notes []string
	)
	keep := func(src, name string) {
		dst := caseID + "-claude-debug-" + name
		truncated, err := copyScrubbed(src, filepath.Join(h.cfg.outDir, dst), h.scr)
		if err != nil {
			notes = append(notes, fmt.Sprintf("Claude debug log %s not kept: %v", name, err))
			return
		}
		files = append(files, dst)
		if truncated > 0 {
			notes = append(notes, fmt.Sprintf("Claude debug log %s: its first %d bytes were dropped (kept the last %d)", dst, truncated, maxDebugCopyBytes))
		}
	}
	if leadFile != "" {
		if fi, err := os.Stat(leadFile); err == nil && fi.Mode().IsRegular() {
			keep(leadFile, "lead.txt")
		} else {
			notes = append(notes, "Claude debug log: the lead wrote none to its --debug-file")
		}
	}
	after := h.debugLogStamps()
	names := make([]string, 0, len(after))
	for n := range after {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if was, ok := before[n]; ok && was == after[n] {
			continue
		}
		keep(filepath.Join(h.claudeDebugDir(), n), n)
	}
	return files, notes
}

// copyScrubbed copies src to dst (created, mode 0600) through
// scrubEvidence. A file over maxDebugCopyBytes keeps its tail from the
// first whole line on (so no value or line is cut in two before scrubbing);
// truncated is how many leading bytes were dropped.
func copyScrubbed(src, dst string, scr scrubber) (truncated int64, err error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() > maxDebugCopyBytes {
		truncated = fi.Size() - maxDebugCopyBytes
		if _, err := f.Seek(truncated, io.SeekStart); err != nil {
			return 0, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxDebugCopyBytes))
	if err != nil {
		return 0, err
	}
	if truncated > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			nl = len(b) - 1
		}
		b, truncated = b[nl+1:], truncated+int64(nl+1)
	}
	return truncated, os.WriteFile(dst, []byte(scr.scrubEvidence(string(b))), 0o600)
}
