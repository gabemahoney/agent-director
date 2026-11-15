package probe

import "errors"

// TerminateIdentity is the identity-checked keyless shutdown signal (SR-6.4):
// SIGTERM to the process pid only while it still has start time start, the
// identity the caller recorded, judged by the start-time reader at the signal
// boundary, so an adopted replacement or a reused pid is not signalled. On
// Linux that holds outright; on Darwin it holds except in the gap between the
// check and the signal (see below and signal_darwin.go: an accepted SR-6.4
// exception, SRD t1.kii.kn `## Guide exceptions`). Exactly one of:
//
//   - nil: the signal was sent to that process;
//   - ErrSignalTargetGone: the process is gone or the pid names another
//     process now; nothing was sent;
//   - ErrSignalTargetUnverifiable: the identity cannot be read, start is
//     empty or pid is not positive; nothing was sent;
//   - ErrProbeUnsupported: this OS has no identity-checked signal; nothing was
//     sent;
//   - another error: the signal call failed, and whether it was delivered is
//     not known.
//
// On Linux the process is pinned by a pidfd opened before the identity check,
// so the check and the signal name the same process. Darwin has no pidfd: the
// signal follows the check at once, with nothing between them, but if the
// checked process exits and its pid is reused in that gap the signal reaches
// the new process. This platform limitation is the accepted exception "Darwin
// keyless pause may signal a reused PID in a narrow gap" (SRD t1.kii.kn
// `## Guide exceptions`, E2 decision 17).
func TerminateIdentity(pid int, start string) error {
	if pid <= 0 || start == "" {
		return ErrSignalTargetUnverifiable
	}
	return terminateIdentity(pid, start)
}

// ErrSignalTargetGone is TerminateIdentity's answer when the identified
// process no longer runs: nothing was signalled.
var ErrSignalTargetGone = errors.New("probe: signal target gone")

// ErrSignalTargetUnverifiable is TerminateIdentity's answer when the
// process's identity cannot be established: nothing was signalled.
var ErrSignalTargetUnverifiable = errors.New("probe: signal target identity cannot be verified")

// checkSignalIdentity judges pid against start with the start-time reader,
// for the signal: nil only for the same live process.
func checkSignalIdentity(pc ProcChecker, pid int, start string) error {
	got, alive, known := pc.StartTime(pid)
	switch {
	case !known:
		return ErrSignalTargetUnverifiable
	case !alive || got != start:
		return ErrSignalTargetGone
	}
	return nil
}
