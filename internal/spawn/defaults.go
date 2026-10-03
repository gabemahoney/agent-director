package spawn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/google/uuid"
)

// CollisionChecker is the narrow store surface ApplyDefaults needs: the one
// pre-check read, which returns the state of the row with the given
// claude_instance_id, exists false when there is no row, and an error only
// when the store cannot be read. So the one read tells no row, a live row
// and a finished row apart (SR-9.3). A live state (see store.IsLiveState)
// becomes ErrInstanceIdCollision; a read error, whatever it wraps, becomes
// PreCheckReadError's uncatalogued error (ErrInternal on every surface),
// never a collision and never "no row". Production callers pass
// *store.Store; tests pass a fake or a failing wrapper to drive either
// outcome.
type CollisionChecker interface {
	SpawnState(instanceID string) (state string, exists bool, err error)
}

// IDCheck is what ApplyDefaults found for the instance id: whether it minted
// it, and for a caller-supplied id, what the collision pre-check read (SR-9.3).
// The zero value is not a valid answer.
type IDCheck int

// The pre-check's answers.
const (
	// IDMinted: the caller supplied no id and a fresh UUID4 was minted; no
	// store read was made. A fresh random id cannot have a leftover, so it is
	// never scanned.
	IDMinted IDCheck = iota + 1
	// IDNoRow: a caller-supplied id with no row of any state: the plain
	// spawn's label scan runs for it.
	IDNoRow
	// IDFinishedRow: a caller-supplied id whose row is finished (not live):
	// not scanned; without the reuse parameter the insert still collides
	// with ErrInstanceIdCollision (AC-SPN-03).
	IDFinishedRow
	// IDNotChecked: a caller-supplied id and no CollisionChecker given; no
	// read was made.
	IDNotChecked
)

// PreCheckReadError is the single mapping (SR-1.8) of a failed collision
// pre-check store read. The result wraps no sentinel, so it matches no
// catalogued error and every surface classifies it as ErrInternal
// (SR-9.3). A caller must never read a store fault as "the id is taken,
// resume instead".
//
// The store error follows as plain text for diagnosis. It is formatted
// with %v, not wrapped with %w, so no sentinel in its chain can leak
// through errors.Is.
//
// Every collision pre-check read, the plain spawn's here and any later
// one, maps its failure through this function rather than building its
// own error.
func PreCheckReadError(err error) error {
	return fmt.Errorf("the collision pre-check could not read the store: %v", err)
}

// ApplyDefaults fills SRD §7.3 defaults and runs the SRD §7.2 step 6
// collision check (the only validation step that needs DB access). The
// function takes a CollisionChecker rather than the full store so tests
// can drive it without spinning up SQLite. It returns what it found for the
// instance id, which tells the caller whether the label scan applies and
// whether to pass Launch minted (IDMinted).
//
// Behavior:
//   - ClaudeInstanceID ← UUID4 if absent (IDMinted). UUID4 from
//     github.com/google/uuid reads crypto/rand under the hood (not
//     math/rand). An empty id never consults the store.
//   - TmuxSessionName ← <sanitize(basename(cwd))>-<id[:8]>. The sanitizer
//     replaces every char outside [A-Za-z0-9_-] with `-`; an empty or
//     all-dashes result collapses to the literal `root`.
//   - RelayMode ← cfg.Defaults.RelayMode if the caller left it empty.
//   - Caller-supplied ClaudeInstanceID triggers one pre-check read of the
//     row's state. A live row (`pending` included) returns
//     ErrInstanceIdCollision. A failed read returns PreCheckReadError's
//     error, which surfaces as ErrInternal, not as a collision. Either way
//     nothing is created: the pre-check runs before Launch, so no row, no
//     pre-trust write and no tmux call follow. No row gives IDNoRow and a
//     finished row IDFinishedRow; SQLite's PRIMARY KEY catches the finished
//     row, and any TOCTOU race, at INSERT.
func ApplyDefaults(r *Resolved, cfg config.Config, checker CollisionChecker) (IDCheck, error) {
	check, err := preCheckID(r, checker)
	if err != nil {
		return 0, err
	}
	if r.TmuxSessionName == "" {
		r.TmuxSessionName = composeSessionName(r.CWD, r.ClaudeInstanceID)
	}
	if r.RelayMode == "" {
		r.RelayMode = cfg.Defaults.RelayMode
	}
	return check, nil
}

// preCheckID mints an absent instance id, or runs the collision pre-check's
// one read for a caller-supplied one (SR-9.3).
func preCheckID(r *Resolved, checker CollisionChecker) (IDCheck, error) {
	if r.ClaudeInstanceID == "" {
		r.ClaudeInstanceID = uuid.NewString()
		return IDMinted, nil
	}
	if checker == nil {
		return IDNotChecked, nil
	}
	state, exists, err := checker.SpawnState(r.ClaudeInstanceID)
	switch {
	case err != nil:
		return 0, PreCheckReadError(err)
	case !exists:
		return IDNoRow, nil
	case store.IsLiveState(state):
		return 0, fmt.Errorf("%w: %s already live", ErrInstanceIdCollision, r.ClaudeInstanceID)
	}
	return IDFinishedRow, nil
}

// composeSessionName builds the canonical session name from the canonical
// cwd basename plus the first 8 chars of the instance ID; both segments are
// passed through SanitizeSessionName (same slug rules). Stable input →
// stable name, so resume can re-derive it.
func composeSessionName(cwd, instanceID string) string {
	base := filepath.Base(cwd)
	slug := SanitizeSessionName(base)
	idTail := instanceID
	if len(idTail) > 8 {
		idTail = idTail[:8]
	}
	idTail = SanitizeSessionName(idTail)
	return slug + "-" + idTail
}

// SanitizeSessionName implements the SRD §7.3 sanitizer: any char outside
// [A-Za-z0-9_-] becomes `-`. Empty / all-dashes results collapse to
// `root` so the final session name is never empty and never starts with
// a separator.
func SanitizeSessionName(in string) string {
	if in == "" {
		return "root"
	}
	var b strings.Builder
	b.Grow(len(in))
	for _, r := range in {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if strings.Trim(s, "-") == "" {
		return "root"
	}
	return s
}
