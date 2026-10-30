package inputjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LegacyJournal retains the original synchronization inode until its owner has
// verified committed SQLite authority. Close never removes or replaces it.
type LegacyJournal struct {
	Dir   string
	lock  *os.File
	files map[string][]byte
}

func OpenLegacyJournal(dir string) (*LegacyJournal, error) {
	if !filepath.IsAbs(dir) || !hexID.MatchString(filepath.Base(dir)) {
		return nil, ErrInvalidState
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: legacy launch directory unavailable", ErrInvalidState)
	}
	lockPath := dir + ".lock"
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: legacy writer lock unavailable", ErrInvalidState)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := LockWriter(f); err != nil {
		f.Close()
		return nil, err
	}
	return &LegacyJournal{Dir: dir, lock: f}, nil
}

func (j *LegacyJournal) Close() error { return j.lock.Close() }

func decodeLegacy(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ErrInvalidState
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrInvalidState
	}
	return nil
}

// Read takes every operational file while holding the original lock. Files
// containing damaged/ambiguous ownership remain exact private import evidence;
// unreadable files refuse the migration instead of becoming an empty launch.
func (j *LegacyJournal) Read(ctx context.Context, storeID string, owner *Identity) (ImportRecord, error) {
	r := ImportRecord{Source: j.Dir, Launch: Launch{Identity: Identity{StoreID: storeID, Token: filepath.Base(j.Dir)}, EvidenceDir: j.Dir, Revision: 1}}
	entries, err := os.ReadDir(j.Dir)
	if err != nil {
		return r, err
	}
	j.files = make(map[string][]byte)
	var total int
	for _, e := range entries {
		name := e.Name()
		if name != "launch.json" && name != "attempt.json" && !(strings.HasPrefix(name, "request-") && strings.HasSuffix(name, ".json")) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		info, err := os.Lstat(filepath.Join(j.Dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return r, fmt.Errorf("legacy operational evidence is not readable regular data")
		}
		data, err := ReadEvidence(filepath.Join(j.Dir, name), 8<<20)
		if err != nil {
			return r, err
		}
		total += len(data)
		if total > 64<<20 || len(j.files) >= 4096 {
			return r, fmt.Errorf("legacy launch exceeds bounded import capacity")
		}
		j.files[name] = data
	}
	r.Evidence, err = json.Marshal(j.files) // []byte values preserve source bytes.
	if err != nil {
		return r, err
	}
	r.SourceSHA256 = Hash(r.Evidence)
	block := func(reason string) (ImportRecord, error) {
		r.Launch.Blocker = reason
		r.Launch.CurrentRequestID, r.Attempts = "", nil
		return r, nil
	}
	if data, ok := j.files["launch.json"]; !ok || decodeLegacy(data, &r.Launch.Capability) != nil {
		return block("corrupt_capability")
	}
	m := r.Launch.Capability
	if m.Token != r.Launch.Identity.Token {
		return r, fmt.Errorf("legacy capability claims another launch; restart administrator verification")
	}
	r.Launch.Identity.InstanceID, r.Launch.Identity.Socket, r.Launch.Identity.Agent, r.Launch.Identity.CWD = m.InstanceID, m.Socket, m.Agent, m.CWD
	var current *Attempt
	if data, ok := j.files["attempt.json"]; ok {
		current = new(Attempt)
		if decodeLegacy(data, current) != nil {
			return block("corrupt_current")
		}
		// The original attempt's process is retained even after its row was
		// reused/deleted. Never replace it with today's process identity.
		i := current.Identity
		if i.StoreID != storeID || i.Token != m.Token || i.InstanceID != m.InstanceID || i.Socket != m.Socket || i.Agent != m.Agent || i.CWD != m.CWD {
			return r, fmt.Errorf("legacy attempt claims another owner; restart administrator verification")
		}
		r.Launch.Identity = i
	}
	if owner != nil {
		if owner.StoreID != storeID || owner.Token != m.Token || owner.InstanceID != m.InstanceID || owner.Socket != m.Socket || owner.Agent != m.Agent || owner.CWD != m.CWD {
			return block("conflicting_owner")
		}
		if current != nil && (current.Identity.PaneID != owner.PaneID || current.Identity.PID != owner.PID || current.Identity.Starttime != owner.Starttime) {
			return block("conflicting_process")
		}
		r.Launch.Identity = *owner
	} else {
		// Historical files have no life field. Without the exact current row
		// the life cannot be reconstructed from an instance id or timestamp.
		return block("unresolved_life")
	}
	if err := ValidateLaunch(r.Launch); err != nil {
		return block("invalid_capability")
	}
	requests := make(map[string]*Attempt)
	var names []string
	for name := range j.files {
		if strings.HasPrefix(name, "request-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if current != nil && name == "request-"+Hash([]byte(current.RequestID))+".json" {
			// The current file was replaced first, even if an interrupted
			// history write left its older duplicate malformed.
			continue
		}
		var a Attempt
		if decodeLegacy(j.files[name], &a) != nil || name != "request-"+Hash([]byte(a.RequestID))+".json" {
			return block("corrupt_history")
		}
		if current != nil && a.RequestID == current.RequestID {
			// Save wrote current first. Its older historical copy is not the
			// owner, even when that copy's phase/proof differs.
			continue
		}
		requests[a.RequestID] = &a
	}
	if current != nil {
		requests[current.RequestID] = current
		r.Launch.CurrentRequestID = current.RequestID
	} else if len(requests) != 0 {
		return block("missing_current")
	}
	for _, a := range requests {
		i := a.Identity
		if i.StoreID != storeID || i.Token != m.Token {
			return r, fmt.Errorf("legacy history claims another launch; restart administrator verification")
		}
		i.LifeNumber = r.Launch.Identity.LifeNumber
		if i != r.Launch.Identity {
			return block("conflicting_history")
		}
		a.Identity, a.Revision, a.Confirmation = i, 1, Evidence{}
		if err := ValidateAttempt(r.Launch, a); err != nil {
			return block("invalid_attempt_or_proof")
		}
	}
	for _, a := range requests {
		r.Attempts = append(r.Attempts, a)
	}
	sort.Slice(r.Attempts, func(i, k int) bool { return r.Attempts[i].RequestID < r.Attempts[k].RequestID })
	return r, nil
}

// Retire removes only the exact operational bytes imported by this lock owner.
// Logs/transcripts, the launch directory and the lock inode remain in place.
// Failure cannot revoke SQLite authority; the caller reports it for operators.
func (j *LegacyJournal) Retire(ctx context.Context) error {
	for name, expected := range j.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(j.Dir, name)
		data, err := ReadEvidence(path, 8<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(data, expected) {
			return fmt.Errorf("legacy evidence changed after cutover; files retained")
		}
	}
	for name := range j.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(j.Dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := os.Open(j.Dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
