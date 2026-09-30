package spawn

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The launch token and the launch socket, shared by every write that begins
// a launch: a plain spawn's insert, and later reuse's reset and resume's move
// to pending (SR-3.3, SR-3.5, SR-5.2). Each mapping lives here once (SR-1.8).

// launchTokenBytes is the launch token's size: 64 random bits, written as 16
// lowercase hexadecimal characters (SR-3.5).
const launchTokenBytes = 8

// randRead is the token's randomness source, crypto/rand's Read. Held as a
// var so tests can make it fail; there is never a fallback to a weaker source.
var randRead = rand.Read

// resolveTmuxSocket is tmux.ResolveSocket, held as a var so tests can hand
// back any *tmux.SocketDirError without root or a real per-user directory.
var resolveTmuxSocket = tmux.ResolveSocket

// ensureSocketDir is tmux.EnsureSocketDir, held as a var so tests can hand
// back any *tmux.SocketDirError for a recorded socket without root or a real
// per-user directory.
var ensureSocketDir = tmux.EnsureSocketDir

// NewLaunchToken mints a new launch token: 8 bytes from crypto/rand,
// hex-encoded in lowercase (16 characters; SR-3.5). A read failure is
// returned as an error, which the caller reports as an uncatalogued internal
// error before any write; no weaker source is ever used. The token goes into
// the write that begins the launch and into the session's label (SR-3.3,
// SR-3.5; RN-5).
func NewLaunchToken() (string, error) {
	var b [launchTokenBytes]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("spawn: launch token: read crypto/rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ResolveLaunchSocket returns the absolute tmux socket a fresh launch (a
// plain spawn) records and creates its session on: the socket tmux itself
// would use in the caller's environment, through tmux.ResolveSocket with
// create true, which makes a missing per-user directory with mode 0700 and
// checks it as tmux does (SR-3.3, SR-3.5; RN-5).
//
// A per-user directory that cannot be created or fails tmux's check (a
// symlink, not a directory, not owned by this user, or open to others), and a
// TMUX_TMPDIR naming a regular file, give an error that matches
// tmux.ErrTmuxNotAvailable and no other sentinel. Its description names the
// socket, its directory and the reason, in tmux's own words where tmux has
// them, and says that nothing was launched (SR-1.4 row "an unusable socket
// directory"). The caller returns it before its insert.
func ResolveLaunchSocket() (string, error) {
	return resolveSocket(true)
}

// ResolveScanSocket returns the socket a plain spawn's label scan looks up
// (SR-9.3): the same resolution as ResolveLaunchSocket, but creating nothing
// (tmux.ResolveSocket with create false; LFR H1). A missing per-user
// directory is no error; its would-be socket path is returned. A refusal is
// described and classified exactly as ResolveLaunchSocket's (SR-1.4, SR-3.3;
// RN-5).
func ResolveScanSocket() (string, error) {
	return resolveSocket(false)
}

// ResolveRowLaunchSocket returns the tmux socket a launch onto an existing
// row (resume, and later reuse) creates its session on, given the row's
// recorded tmux_socket (SR-3.3; LFR H1):
//
//   - A recorded socket is returned exactly as recorded, after
//     tmux.EnsureSocketDir: a directory that exists is left unchecked; a
//     vanished per-user directory, tmux-<real uid> under an existing parent,
//     is created again with mode 0700 and checked as tmux checks it (after a
//     reboot, for example). Any other missing directory is refused.
//   - No recorded socket (a row from before the release) resolves one from
//     the caller's environment exactly as a plain spawn does
//     (ResolveLaunchSocket), and the absolute path is returned for the launch
//     to record.
//
// A refusal matches tmux.ErrTmuxNotAvailable and no other sentinel (SR-1.5).
// Its description, built from the typed *tmux.SocketDirError by the same
// builder as ResolveLaunchSocket's, names the socket, its directory and the
// reason (tmux's own words where tmux has them, or that the missing directory
// is not a per-user directory agent-director may create) and says that
// nothing was launched (SR-1.4). The caller returns it before its write.
// ResolveRowLaunchSocket makes no tmux call and writes nothing to the store.
func ResolveRowLaunchSocket(recorded string) (string, error) {
	if recorded == "" {
		return ResolveLaunchSocket()
	}
	if err := ensureSocketDir(recorded); err != nil {
		return "", socketRefusal(err)
	}
	return recorded, nil
}

// resolveSocket is the one resolution and refusal mapping behind
// ResolveLaunchSocket and ResolveScanSocket.
func resolveSocket(create bool) (string, error) {
	socket, err := resolveTmuxSocket(create)
	if err != nil {
		return "", socketRefusal(err)
	}
	return socket, nil
}

// socketRefusal describes a socket resolution or recorded-socket refusal
// (tmux.ResolveSocket, tmux.EnsureSocketDir) from the typed
// *tmux.SocketDirError's fields, never by parsing its text (SR-1.4). It wraps
// the SocketDirError, so the result matches tmux.ErrTmuxNotAvailable and no
// other sentinel (SR-1.5), and its text ends with tmux's own words. An error
// of any other type, which neither returns, is wrapped as it is.
func socketRefusal(err error) error {
	var sde *tmux.SocketDirError
	if !errors.As(err, &sde) {
		return fmt.Errorf("spawn: resolve tmux socket: %w", err)
	}
	return fmt.Errorf("tmux socket %s cannot be used because its directory %s %s, so nothing was launched: %w",
		sde.Socket, sde.Dir, sde.Reason, sde)
}
