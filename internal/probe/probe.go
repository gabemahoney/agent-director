// Package probe reads facts about a single process by pid. It holds three
// readers, all selected by build tags at compile time:
//
//   - The start-time reader (ProcChecker / NewProcChecker, starttime.go;
//     SR-3.8, LFR C1): the one process reader every process judgement uses —
//     the tmux server check, the identity write and adoption, the SR-22.9 hook
//     gate, find-missing liveness, kill's wait, expire's process_alive,
//     resume's and reuse's process check, and the launch owner a launch
//     records and find-missing checks (b.kdf). It answers alive (with the
//     start time), gone (no such process, or a zombie) or unreadable.
//   - The command-name reader (CommandNameReader / NewCommandNameReader,
//     commname.go): fills `ad.hook.ignored`'s `parent_command` and
//     `ad.hook.pane_is_grandparent`'s `pane_command` trail fields only, never
//     evidence.
//   - The parent-pid reader (ParentPIDReader / NewParentPIDReader, ppid.go):
//     feeds the hook's `ad.hook.pane_is_grandparent` check only, never
//     evidence.
//
// Beside them, SelfPIDNamespace (pidns.go) reads the calling process's own
// pid namespace, which a recorded pid is judged in (b.146 rule 14).
//
// Per OS: Linux reads <procRoot>/<pid>/stat and /comm, and the link
// <procRoot>/self/ns/pid (default root /proc); darwin reads the pid's single
// KERN_PROC_PID kinfo_proc entry and has no pid namespaces; any other OS gets
// readers that answer unreadable for every pid.
//
// No reader reads a process environment, and none reads the clock.
// Liveness is judged only by a process's start time (SR-11.1): this package
// has no environment scan and no environment tiebreaker.
package probe

import "errors"

// ErrProbeUnsupported is a catalogued error name (pkg/api/errnames) that no
// verb returns. It is kept, and find-missing's manifest error list still names
// it, so the catalogue and the client error classes stay unchanged (SR-1.7,
// SR-11.7). find-missing no longer scans process environments, the only path
// that returned it.
var ErrProbeUnsupported = errors.New("ErrProbeUnsupported")

// EnvKey is the env-var name agent-director sets in each agent's environment
// to carry its instance id (internal/tmux sets it at launch). It is
// self-identification only: a caller running inside an agent reads it to name
// itself, for example as the parent id of what it spawns. It is never liveness
// or ownership evidence; no reader in this package reads a process
// environment.
const EnvKey = "AGENT_DIRECTOR_INSTANCE_ID"
