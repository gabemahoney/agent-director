// Package runner is the reusable, explicitly invoked Claude live-test fixture.
// Qualification phases share its deadlines, command log, private evidence and
// resource ownership. It is never linked into agent-director.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Schema = 1
const MaxArtifactBytes = 64 << 20

type Result struct {
	SignalID string   `json:"signal_id"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason"`
	Expected any      `json:"expected"`
	Observed any      `json:"observed"`
	Evidence []string `json:"evidence"`
}

type Verdict struct {
	Schema          int            `json:"schema"`
	RunID           string         `json:"run_id"`
	Phase           string         `json:"phase"`
	Candidate       string         `json:"candidate"`
	ProviderVersion string         `json:"provider_version"`
	Status          string         `json:"status"`
	Results         []Result       `json:"results"`
	NamedPremises   []NamedPremise `json:"named_premises,omitempty"`
}

// Named premises are disclosure, not measured results or aggregate inputs.
type NamedPremise struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

type ProbePlan struct {
	Version           string `json:"version"`
	PrimaryTurns      int    `json:"primary_turns"`
	ContinuationTurns int    `json:"continuation_turns"`
	ScheduledTurns    int    `json:"scheduled_turns"`
	AggregateCap      int    `json:"aggregate_cap"`
}

func (v *Verdict) Aggregate() {
	v.Status = "pass"
	if len(v.Results) == 0 {
		v.Status = "not_completed"
	}
	for _, r := range v.Results {
		if r.Status == "fail" {
			v.Status = "fail"
			return
		}
		if r.Status != "pass" {
			v.Status = "not_completed"
		}
	}
}

type Artifact struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Owned struct {
	Agent         string    `json:"agent,omitempty"`
	Native        bool      `json:"native,omitempty"`
	TmuxID        string    `json:"tmux_id,omitempty"`
	StoppedAt     time.Time `json:"stopped_at,omitempty"`
	InstanceID    string    `json:"instance_id"`
	LaunchID      string    `json:"launch_id"`
	LaunchToken   string    `json:"launch_token"`
	SessionID     string    `json:"session_id"`
	CWD           string    `json:"cwd,omitempty"`
	Socket        string    `json:"socket"`
	Transcript    string    `json:"transcript"`
	Started       time.Time `json:"started"`
	PID           int       `json:"pid"`
	ProcessStart  string    `json:"process_start"`
	ObservedTurns int64     `json:"observed_turns,omitempty"`
	Exited        bool      `json:"exited"`
}

type Case struct {
	ID            string    `json:"id"`
	Text          string    `json:"text"`
	Mode          string    `json:"mode"`
	RequestID     string    `json:"request_id"`
	Command       string    `json:"command"`
	InstanceID    string    `json:"instance_id"`
	Started       time.Time `json:"started"`
	Ended         time.Time `json:"ended"`
	Completed     bool      `json:"completed"`
	Ready         bool      `json:"ready"`
	ObservedUntil time.Time `json:"observed_until"`
	Progress      []string  `json:"progress,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

type RecordingHook struct {
	Command   string   `json:"command"`
	SHA256    string   `json:"sha256"`
	Events    []string `json:"events"`
	Directory string   `json:"directory"`
	Installed bool     `json:"installed"`
	Removed   bool     `json:"removed"`
}

type Manifest struct {
	Schema           int                 `json:"schema"`
	RunID            string              `json:"run_id"`
	Phase            string              `json:"phase"`
	Candidate        string              `json:"candidate"`
	KitSHA256        string              `json:"kit_sha256"`
	Platform         string              `json:"platform"`
	ProviderVersion  string              `json:"provider_version"`
	Started          time.Time           `json:"started"`
	Deadline         time.Time           `json:"deadline"`
	Finished         time.Time           `json:"finished"`
	TurnBudget       int                 `json:"turn_budget"`
	TurnsReserved    int                 `json:"turns_reserved"`
	ObservedTurns    int64               `json:"observed_turns"`
	ExpectedChecks   []string            `json:"expected_checks"`
	ProbePlan        *ProbePlan          `json:"probe_plan,omitempty"`
	Owned            []Owned             `json:"owned"`
	Cases            []Case              `json:"cases"`
	ContinuationHook *ContinuationHook   `json:"continuation_hook,omitempty"`
	RecordingHook    RecordingHook       `json:"recording_hook"`
	Artifacts        map[string]Artifact `json:"artifacts"`
	Problems         []Result            `json:"problems"`
}

type Evidence struct {
	Root      string
	mu        sync.Mutex
	artifacts map[string]Artifact
}

func NewEvidence(root string) (*Evidence, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("evidence path must be absolute and clean")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, err
	}
	return &Evidence{Root: root, artifacts: map[string]Artifact{}}, nil
}

// Confined rejects links at every component, not just a lexical ../ escape.
func Confined(root, name string) (string, error) {
	if st, err := os.Lstat(root); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("evidence root is a symlink")
	}
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || name == ".." || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("unsafe evidence reference %q", name)
	}
	p := root
	for _, part := range strings.Split(name, "/") {
		p = filepath.Join(p, part)
		st, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink in evidence reference")
		}
	}
	return p, nil
}

func Read(root, name string) ([]byte, error) {
	p, err := Confined(root, name)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxArtifactBytes {
		return nil, fmt.Errorf("evidence %q is not a bounded regular file", name)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxArtifactBytes {
		return nil, errors.New("evidence is not a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxArtifactBytes+1))
	if len(b) > MaxArtifactBytes {
		return nil, errors.New("evidence size limit")
	}
	return b, err
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (e *Evidence) Put(name string, b []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := Confined(e.Root, name)
	if err != nil {
		return err
	}
	if len(b) > MaxArtifactBytes {
		return errors.New("artifact size limit")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	e.artifacts[name] = Artifact{SHA256: Digest(b), Bytes: int64(len(b))}
	return nil
}

func (e *Evidence) JSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return e.Put(name, append(b, '\n'))
}

// Index includes passive recorder files after the agent has exited and the
// recorder registrations have been removed. Final files are never overwritten.
func (e *Evidence) Index() (map[string]Artifact, error) {
	index := map[string]Artifact{}
	err := filepath.WalkDir(e.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		n, err := filepath.Rel(e.Root, p)
		if err != nil {
			return err
		}
		if n == "manifest.json" || n == "verdict.json" {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular artifact %q (%s)", n, d.Type())
		}
		b, err := Read(e.Root, n)
		if err != nil {
			return err
		}
		index[n] = Artifact{SHA256: Digest(b), Bytes: int64(len(b))}
		return nil
	})
	return index, err
}
