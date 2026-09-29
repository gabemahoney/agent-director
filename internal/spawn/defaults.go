package spawn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/google/uuid"
)

// CollisionChecker is the narrow store surface ApplyDefaults needs. It
// returns true when a row with the given claude_instance_id exists in a
// live state (anything except `ended` / `missing`), and an error when the
// store cannot be read. A true result becomes ErrInstanceIdCollision; an
// error becomes PreCheckReadError's uncatalogued error (ErrInternal on
// every surface), never a collision. Production callers pass
// *store.Store; tests pass a fake or a failing wrapper to drive either
// outcome.
type CollisionChecker interface {
	LiveSpawnExists(instanceID string) (bool, error)
}

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
// can drive it without spinning up SQLite.
//
// Behavior:
//   - ClaudeInstanceID ← UUID4 if absent. UUID4 from github.com/google/uuid
//     reads crypto/rand under the hood (not math/rand). An empty id never
//     consults the store.
//   - TmuxSessionName ← <sanitize(basename(cwd))>-<id[:8]>. The sanitizer
//     replaces every char outside [A-Za-z0-9_-] with `-`; an empty or
//     all-dashes result collapses to the literal `root`.
//   - RelayMode ← cfg.Defaults.RelayMode if the caller left it empty.
//   - Caller-supplied ClaudeInstanceID triggers a collision query against
//     the store. A live row (`pending` included) returns
//     ErrInstanceIdCollision. A failed read returns PreCheckReadError's
//     error, which surfaces as ErrInternal, not as a collision. Either way
//     nothing is created: the pre-check runs before Launch, so no row, no
//     pre-trust write and no tmux call follow. SQLite's PRIMARY KEY catches
//     any TOCTOU race at INSERT.
func ApplyDefaults(r *Resolved, cfg config.Config, store CollisionChecker) error {
	if r.ClaudeInstanceID == "" {
		r.ClaudeInstanceID = uuid.NewString()
	} else if store != nil {
		exists, err := store.LiveSpawnExists(r.ClaudeInstanceID)
		if err != nil {
			return PreCheckReadError(err)
		}
		if exists {
			return fmt.Errorf("%w: %s already live", ErrInstanceIdCollision, r.ClaudeInstanceID)
		}
	}
	if r.TmuxSessionName == "" {
		r.TmuxSessionName = composeSessionName(r.CWD, r.ClaudeInstanceID)
	}
	if r.RelayMode == "" {
		r.RelayMode = cfg.Defaults.RelayMode
	}
	return nil
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
