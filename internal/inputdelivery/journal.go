// Package inputdelivery implements the version-pinned ordinary composer
// transport. Queue observations pace writes; only native transcripts prove a turn.
package inputdelivery

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const ChunkBytes = 512

// Identity binds every operation to a single owned launch and process.
type Identity struct {
	StoreID, Token, InstanceID, Socket, PaneID, Starttime, Agent, CWD string
	PID                                                               int
}

// Attempt is durable before each potentially delivered write. Payload is private
// evidence retained for passive reconciliation, never an instruction to replay.
type Attempt struct {
	RequestID          string               `json:"request_id"`
	Identity           Identity             `json:"identity"`
	Payload            string               `json:"payload"`
	PayloadSHA256      string               `json:"payload_sha256"`
	SnapshotAt         time.Time            `json:"snapshot_at,omitempty"`
	NativeSessionID    string               `json:"native_session_id,omitempty"`
	Watermark          map[string]Watermark `json:"watermark"`
	Phase              string               `json:"phase"`
	BytesAttempted     int                  `json:"bytes_attempted"`
	EnterAttempted     bool                 `json:"enter_attempted"`
	ManualInterference bool                 `json:"manual_interference"`
	LastQueue          *int                 `json:"last_queue,omitempty"`
	Proof              *Proof               `json:"proof,omitempty"`
}

type Watermark struct {
	Size           int64
	PrefixSHA256   string
	SessionID      string
	MetadataSHA256 string
}
type Proof struct {
	Path, SessionID, RecordID, RawSHA256 string
	Offset                               int64
	// A byte slice is base64 in the journal: nesting a RawMessage would compact
	// whitespace and HTML-escape evidence after its exact bytes were hashed.
	// Legacy object-valued Raw fields fail decoding rather than becoming proof.
	Raw []byte
}

// Session is held exclusively across automatic and manual AD writers. A crash
// releases the OS lock but leaves the last durable attempt for observation only.
type Session interface {
	Close() error
	Load() (*Attempt, error)
	LoadRequest(string) (*Attempt, error)
	Save(*Attempt) error
	Ready(Identity) (bool, error)
	Queue(Identity) (int, error)
	Watermark(Identity) (map[string]Watermark, error)
	Confirm(*Attempt) (*Proof, error)
}

type Backend interface {
	Open(Identity) (Session, error)
}
type Native struct{}

type manifest struct {
	Agent, Version, SessionID, CWD, Socket, Token, InstanceID, Transcript string
	Created                                                               time.Time
	Safe                                                                  bool
	KeymapPinned                                                          bool
}

type nativeSession struct {
	dir      string
	lock     *os.File
	manifest manifest
}

var hexID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func launchDir(storeID, token string) (string, error) {
	if !hexID.MatchString(storeID) || !hexID.MatchString(token) {
		return "", fmt.Errorf("launch identity is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agent-director", "input", storeID, token), nil
}

// OpenExisting acquires ownership only when native launch state already exists.
// Absence is a capability miss, while unreadable or corrupt existing state must
// fail closed so an unresolved native attempt can never fall back to replay.
func (n Native) OpenExisting(id Identity) (Session, error) {
	if !hexID.MatchString(id.StoreID) || !hexID.MatchString(id.Token) {
		return nil, nil
	}
	dir, err := launchDir(id.StoreID, id.Token)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return n.Open(id)
}

func (Native) Open(id Identity) (Session, error) {
	dir, err := launchDir(id.StoreID, id.Token)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockWriter(f); err != nil {
		f.Close()
		return nil, err
	}
	s := &nativeSession{dir: dir, lock: f}
	data, err := os.ReadFile(filepath.Join(dir, "launch.json"))
	if err == nil {
		err = json.Unmarshal(data, &s.manifest)
	}
	if err != nil && !os.IsNotExist(err) {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *nativeSession) Close() error { return s.lock.Close() }
func (s *nativeSession) Load() (*Attempt, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "attempt.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a Attempt
	if err = json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if a.RequestID == "" || a.Phase == "" {
		return nil, fmt.Errorf("invalid delivery journal")
	}
	if err = s.validateAttempt(&a); err != nil {
		return nil, err
	}
	return &a, nil
}
func (s *nativeSession) LoadRequest(id string) (*Attempt, error) {
	current, err := s.Load()
	if err != nil || current != nil && current.RequestID == id {
		return current, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "request-"+Hash([]byte(id))+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a Attempt
	if err = json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if a.RequestID != id {
		return nil, fmt.Errorf("request journal identity mismatch")
	}
	if err = s.validateAttempt(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *nativeSession) validateAttempt(a *Attempt) error {
	if a.PayloadSHA256 != Hash([]byte(a.Payload)) || a.BytesAttempted < 0 || a.BytesAttempted > len(a.Payload) {
		return fmt.Errorf("invalid delivery payload journal")
	}
	switch a.Phase {
	case "not_started", "attempt_started", "text_attempted", "enter_attempted", "manual_input", "submitted":
	default:
		return fmt.Errorf("unknown delivery phase")
	}
	if a.Phase == "submitted" {
		p := a.Proof
		if p == nil || p.RawSHA256 != Hash(p.Raw) || !a.EnterAttempted {
			return fmt.Errorf("invalid stored submission proof")
		}
		if _, match := s.matchRecord(p.Raw, a, p.SessionID); !match {
			return fmt.Errorf("stored user record does not prove this payload")
		}
	}
	return nil
}

func (s *nativeSession) Save(a *Attempt) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	// Current ownership must commit first. A crash between these files may
	// lose a history update but can never expose an earlier settled owner.
	if err := writeJSON(s.dir, "attempt.json", a); err != nil {
		return err
	}
	return writeJSON(s.dir, "request-"+Hash([]byte(a.RequestID))+".json", a)
}

// Atomic replacement plus directory fsync makes intent durable before input.
func writeJSON(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".intent-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func NewAttempt(id Identity, text string, watermark map[string]Watermark) (*Attempt, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return &Attempt{RequestID: hex.EncodeToString(buf), Identity: id, Payload: text, PayloadSHA256: Hash([]byte(text)), SnapshotAt: time.Now().UTC(), Watermark: watermark, Phase: "attempt_started"}, nil
}

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Chunks rejects terminal controls and command modes before any bytes are sent.
// LF is preserved, CR is removed by the API before this validation.
func Chunks(text string) ([]string, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("ordinary text must be valid UTF-8")
	}
	trim := strings.TrimLeftFunc(text, unicode.IsSpace)
	if trim == "" || strings.HasPrefix(trim, "/") || strings.HasPrefix(trim, "!") {
		return nil, fmt.Errorf("empty input and command modes require a manual pane operation")
	}
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n') || r == 0x2028 || r == 0x2029 {
			return nil, fmt.Errorf("unsupported ordinary-text control character")
		}
	}
	var chunks []string
	for len(text) > 0 {
		n := min(ChunkBytes, len(text))
		for n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		chunks = append(chunks, text[:n])
		text = text[n:]
	}
	return chunks, nil
}
