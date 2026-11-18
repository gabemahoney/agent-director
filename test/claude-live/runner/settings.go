package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

type Settings struct {
	path         string
	lock         *os.File
	added        map[string]json.RawMessage
	absent       bool
	hooksAbsent  bool
	eventsAbsent map[string]bool
}

type settingsLedger struct {
	RunID        string                     `json:"run_id"`
	Absent       bool                       `json:"absent"`
	HooksAbsent  bool                       `json:"hooks_absent"`
	Added        map[string]json.RawMessage `json:"added"`
	EventsAbsent map[string]bool            `json:"events_absent"`
}

func settingsObject(path string) (map[string]json.RawMessage, []byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var obj map[string]json.RawMessage
	if err = json.Unmarshal(b, &obj); err != nil || obj == nil {
		return nil, nil, errors.New("Claude settings must be an object")
	}
	return obj, b, nil
}

func replaceSettings(path string, prior []byte, obj map[string]json.RawMessage) error {
	_, current, err := settingsObject(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(prior, current) {
		return errors.New("settings changed concurrently; retained ledger for recovery")
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude-live-settings-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// InstallRecorder preserves raw unrelated setting values and appends only the
// requested event entries. The lock/ledger is outside exported evidence and
// holds only additions, never a credential-bearing settings backup.
func InstallRecorder(home, executable, root, runID string, events []string) (*Settings, error) {
	return installRecorderAt(VMRoot, home, executable, root, runID, events)
}

func installRecorderAt(runRoot, home, executable, root, runID string, events []string) (*Settings, error) {
	if err := validateHome(home, filepath.Join(runRoot, "home")); err != nil {
		return nil, err
	}
	path, err := Confined(home, ".claude/settings.json")
	if err != nil {
		return nil, err
	}
	return installRecorderFile(path, executable, root, runID, events)
}

// installRecorderFile is the filesystem seam for isolated runner tests. Live
// callers enter through installRecorderAt's scoped HOME validation.
func installRecorderFile(path, executable, root, runID string, events []string) (*Settings, error) {
	var err error
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".claude-live.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another live recorder owns settings")
	}
	s := &Settings{path: path, lock: lock, added: map[string]json.RawMessage{}, eventsAbsent: map[string]bool{}}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	ledgerPath := path + ".claude-live-ledger"
	if _, err = os.Lstat(ledgerPath); !os.IsNotExist(err) {
		return nil, errors.New("unrecovered recorder ledger; inspect before another run")
	}
	obj, prior, err := settingsObject(path)
	if err != nil {
		return nil, err
	}
	s.absent = prior == nil
	hooks := map[string]json.RawMessage{}
	h, exists := obj["hooks"]
	s.hooksAbsent = !exists
	if exists {
		if err = json.Unmarshal(h, &hooks); err != nil || hooks == nil {
			return nil, errors.New("hooks must be an object")
		}
	}
	for _, event := range events {
		if _, dup := s.added[event]; dup {
			return nil, errors.New("duplicate recorder event")
		}
		var entries []json.RawMessage
		old, exists := hooks[event]
		s.eventsAbsent[event] = !exists
		if exists {
			if bytes.Equal(bytes.TrimSpace(old), []byte("null")) {
				return nil, errors.New("hook event must be an array")
			}
			if err = json.Unmarshal(old, &entries); err != nil {
				return nil, err
			}
		}
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": executable, "args": []string{"record-hook", "--evidence", root, "--run-id", runID, "--event", event}, "timeout": 3}}}
		b, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		s.added[event] = b
		entries = append(entries, b)
		hooks[event], err = json.Marshal(entries)
		if err != nil {
			return nil, err
		}
	}
	obj["hooks"], err = json.Marshal(hooks)
	if err != nil {
		return nil, err
	}
	ledger, err := json.Marshal(settingsLedger{runID, s.absent, s.hooksAbsent, s.added, s.eventsAbsent})
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(ledgerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(ledger)
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if err = replaceSettings(path, prior, obj); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func equalJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func (s *Settings) Remove() error {
	defer s.Close()
	obj, prior, err := settingsObject(s.path)
	if err != nil {
		return err
	}
	var hooks map[string]json.RawMessage
	if err = json.Unmarshal(obj["hooks"], &hooks); err != nil {
		return err
	}
	for event, entry := range s.added {
		var rows []json.RawMessage
		if err = json.Unmarshal(hooks[event], &rows); err != nil {
			return err
		}
		kept := rows[:0]
		removed := 0
		for _, r := range rows {
			if equalJSON(r, entry) {
				removed++
				continue
			}
			kept = append(kept, r)
		}
		if removed != 1 {
			return fmt.Errorf("recorder %s changed or missing; inspect private ledger", event)
		}
		if len(kept) == 0 && s.eventsAbsent[event] {
			delete(hooks, event)
		} else {
			hooks[event], err = json.Marshal(kept)
			if err != nil {
				return err
			}
		}
	}
	if len(hooks) == 0 && s.hooksAbsent {
		delete(obj, "hooks")
	} else {
		obj["hooks"], err = json.Marshal(hooks)
		if err != nil {
			return err
		}
	}
	if err = replaceSettings(s.path, prior, obj); err != nil {
		return err
	}
	if s.absent && len(obj) == 0 {
		if err = os.Remove(s.path); err != nil {
			return err
		}
	}
	return os.Remove(s.path + ".claude-live-ledger")
}

func (s *Settings) Close() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}
