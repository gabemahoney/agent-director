package probe

// CommandNameReader reads a process's command name (SR-14, `ad.hook.ignored`'s
// `parent_command`: "its command name, from /proc/<pid>/comm or the darwin
// equivalent").
//
// It is used ONLY to fill `ad.hook.ignored`'s `parent_command` and
// `ad.hook.pane_is_grandparent`'s `pane_command` trail fields, so a human
// reading the trail can see what the ignored hook's parent process was (for
// example `sh`, `dash` or a nested `claude`) and what the row's pane process
// was. It is NEVER evidence: no ownership, launch, liveness or hook-gate
// decision reads it — the hook gate compares only the parent's pid and start
// time (SR-22.9, via ProcChecker.StartTime).
//
// Contract of CommandName(pid):
//
//   - (name, true):  the process's command name as the kernel reports it
//     (Linux: /proc/<pid>/comm without its trailing newline, at most 15
//     bytes; darwin: the KERN_PROC_PID entry's p_comm, at most 16 bytes).
//   - ("", false):   anything else — a non-positive pid, no such process, a
//     permission wall, an unreadable or malformed entry, layout drift, an
//     unsupported OS.
//
// The reader never fails loudly (no error, no panic, no log), never reads a
// process environment and never reads the clock.
type CommandNameReader interface {
	CommandName(pid int) (name string, ok bool)
}

// NewCommandNameReader returns the per-OS command-name reader, selected by
// build tags at compile time (mirroring NewProcChecker):
//
//   - Linux:  linuxCommandNameReader over the default /proc root.
//   - darwin: darwinCommandNameReader over the real per-pid KERN_PROC_PID
//     fetch the start-time reader uses.
//   - other:  unsupportedCommandNameReader, which answers ("", false) for
//     every pid.
func NewCommandNameReader() CommandNameReader {
	return newCommandNameReader()
}

// unsupportedCommandNameReader is the command-name reader on an OS with no
// per-OS implementation: ("", false) for every pid. Build-tag-free so its
// contract is unit-testable on any OS; newCommandNameReader returns it only on
// unsupported builds (commname_unsupported.go).
type unsupportedCommandNameReader struct{}

// CommandName always answers ("", false).
func (unsupportedCommandNameReader) CommandName(_ int) (name string, ok bool) {
	return "", false
}
