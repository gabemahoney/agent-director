package installversion

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/layout"
)

// Each intent is durable before selection. Completion is a separate immutable
// record, so neither rollback nor a missing completion can erase an expansion.
type publication struct {
	FormatVersion         int      `json:"format_version"`
	Sequence              uint64   `json:"sequence"`
	Operation             string   `json:"operation"`
	VersionID             string   `json:"version_id"`
	RecordSHA256          string   `json:"record_sha256"`
	PreviousVersionID     *string  `json:"previous_version_id"`
	RequiredCoveredAgents []string `json:"required_covered_agents"`
	complete              bool
	version               string // Stamp from the validated retained record; not serialized.
}

type completion struct {
	FormatVersion int    `json:"format_version"`
	Sequence      uint64 `json:"sequence"`
	IntentSHA256  string `json:"intent_sha256"`
}

func historyName(sequence uint64, kind string) string {
	return fmt.Sprintf("%020d.%s.json", sequence, kind)
}

func (op *operation) readHistory() error {
	dir := filepath.Join(op.root, layout.PublicationsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	intents := map[uint64]bool{}
	completions := map[uint64]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") {
			continue
		}
		parts := strings.Split(name, ".")
		if len(parts) != 3 || parts[2] != "json" {
			return invalid("unexpected publication-history entry")
		}
		n, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || n == 0 || name != historyName(n, parts[1]) {
			return invalid("invalid publication sequence")
		}
		switch parts[1] {
		case "intent":
			intents[n] = true
		case "complete":
			completions[n] = true
		default:
			return invalid("unknown publication-history record")
		}
	}
	if len(intents) == 0 && len(completions) == 0 {
		return nil
	}
	if op.anchor == nil {
		return invalid("publication history has no permanent anchor")
	}
	var previous *string
	for n := uint64(1); n <= uint64(len(intents)); n++ {
		if !intents[n] {
			return invalid("publication history has a gap")
		}
		path := filepath.Join(dir, historyName(n, "intent"))
		data, err := readBytes(path)
		if err != nil {
			return err
		}
		var p publication
		if err := decode(data, &p); err != nil {
			return err
		}
		if p.FormatVersion != FormatVersion || p.Sequence != n || !validDigest(p.VersionID) || !validDigest(p.RecordSHA256) || !validSet(p.RequiredCoveredAgents) || (p.Operation != "publish" && p.Operation != "rollback" && p.Operation != "bootstrap") || !sameOptional(previous, p.PreviousVersionID) {
			return invalid("invalid publication intent")
		}
		v, hash, err := readVersion(op.root, p.VersionID, true)
		if err != nil {
			return err
		}
		if hash != p.RecordSHA256 {
			return invalid("publication intent refers to a changed version record")
		}
		p.version = v.Version
		if err := compatible(v.Facts, *op.anchor, op.floor); err != nil {
			return invalid("publication history violates its permanent contract or coverage floor")
		}
		if !slices.Equal(v.CoveredAgents, p.RequiredCoveredAgents) {
			return invalid("publication intent has an inconsistent coverage floor")
		}
		if completions[n] {
			var c completion
			if err := readJSON(filepath.Join(dir, historyName(n, "complete")), &c); err != nil {
				return err
			}
			if c.FormatVersion != FormatVersion || c.Sequence != n || c.IntentSHA256 != digest(data) {
				return invalid("invalid publication completion")
			}
			p.complete = true
			delete(completions, n)
		} else if n != uint64(len(intents)) {
			return invalid("unfinished publication precedes another intent")
		}
		op.history = append(op.history, p)
		op.floor = slices.Clone(v.CoveredAgents)
		previous = &p.VersionID
	}
	if len(completions) != 0 {
		return invalid("publication completion has no intent")
	}
	return nil
}

func sameOptional(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (op *operation) newIntent(v InstalledVersion, kind string) (publication, error) {
	var p publication
	if uint64(len(op.history)) == math.MaxUint64 {
		return p, invalid("publication sequence exhausted")
	}
	_, hash, err := readVersion(op.root, v.VersionID, true)
	if err != nil {
		return p, err
	}
	p = publication{FormatVersion: FormatVersion, Sequence: uint64(len(op.history)) + 1, Operation: kind, VersionID: v.VersionID, RecordSHA256: hash, RequiredCoveredAgents: slices.Clone(v.CoveredAgents)}
	p.version = v.Version
	if len(op.history) > 0 {
		prior := op.history[len(op.history)-1].VersionID
		p.PreviousVersionID = &prior
	}
	if err := op.writeAtomic(filepath.Join(op.root, layout.PublicationsDir, historyName(p.Sequence, "intent")), p, "history.intent", false); err != nil {
		return p, err
	}
	op.history = append(op.history, p)
	op.floor = slices.Clone(v.CoveredAgents)
	return p, nil
}

func (op *operation) completeIntent(p publication) error {
	dir := filepath.Join(op.root, layout.PublicationsDir)
	data, err := readBytes(filepath.Join(dir, historyName(p.Sequence, "intent")))
	if err != nil {
		return err
	}
	c := completion{FormatVersion, p.Sequence, digest(data)}
	path := filepath.Join(dir, historyName(p.Sequence, "complete"))
	if _, err := os.Lstat(path); err == nil {
		var existing completion
		if err := readJSON(path, &existing); err != nil {
			return err
		}
		if existing != c {
			return invalid("inconsistent publication completion")
		}
		// A crash after completion rename but before its parent sync needs
		// durability again, not an overwrite of the immutable completion.
		return op.syncDir(dir, "history.completion.parent-sync")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := op.writeAtomic(path, c, "history.completion", false); err != nil {
		return err
	}
	op.history[len(op.history)-1].complete = true
	return nil
}
