// Package inputjournal owns the persisted input protocol, exact proof validation
// and legacy import. Store and transport must reuse these records and validators;
// this package never discovers processes, probes terminals or invokes tmux.
package inputjournal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ChunkBytes is the maximum UTF-8 write segment retained by proof matching.
const ChunkBytes = 512

var hexID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ValidID checks the existing store/launch token spelling.
func ValidID(s string) bool { return hexID.MatchString(s) }

// Identity binds every operation to a single owned launch and process.
type Identity struct {
	StoreID, Token, InstanceID, Socket, PaneID, Starttime, Agent, CWD string
	PID                                                               int
	LifeNumber                                                        int64
}

// Attempt is durable before each potentially delivered write. Payload is private
// evidence retained for passive reconciliation, never an instruction to replay.
type Attempt struct {
	Revision           int64                `json:"revision,omitempty"`
	Confirmation       Evidence             `json:"confirmation,omitempty"`
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

// Capability is the pinned provider contract stored with a launch.
type Capability struct {
	Agent, Version, SessionID, CWD, Socket, Token, InstanceID, Transcript string
	Created                                                               time.Time
	Safe                                                                  bool
	KeymapPinned                                                          bool
}

// NewAttempt allocates an observation-only request identity and initial snapshot.
func NewAttempt(id Identity, text string, watermark map[string]Watermark) (*Attempt, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return &Attempt{RequestID: hex.EncodeToString(buf), Identity: id, Payload: text, PayloadSHA256: Hash([]byte(text)), SnapshotAt: time.Now().UTC(), Watermark: watermark, Phase: "attempt_started"}, nil
}

// Hash returns the SHA-256 of exact evidence bytes.
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
