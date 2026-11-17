// Package resumedelegate defines the permanent private resume transport.
// Every later runtime accepts request format 1 and emits the requested response
// format. It transports references, never a parsed version-specific config.
package resumedelegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const (
	Argument = "--resume-delegation-job"
	Format1  = 1
	// MaxBytes bounds the complete wire message, including its newline.
	MaxBytes = 1024 * 1024
)

type Caller struct {
	Process          string `json:"process"`
	PID              int    `json:"pid"`
	Hostname         string `json:"hostname"`
	User             string `json:"user"`
	ParentInstanceID string `json:"parent_instance_id"`
}

type Request struct {
	RequestFormat  int             `json:"request_format"`
	ResponseFormat int             `json:"response_format"`
	Params         json.RawMessage `json:"params"`
	Home           string          `json:"home"`
	StorePath      string          `json:"store_path"`
	StoreID        string          `json:"store_id"`
	ConfigPath     string          `json:"config_path"`
	DefaultConfig  bool            `json:"default_config"`
	TmuxCommand    string          `json:"tmux_command"`
	Caller         Caller          `json:"caller"`
	// Deadline is inherited unchanged by the recipient, including its setup.
	Deadline time.Time `json:"deadline"`
}

type Error struct {
	Name        string `json:"err_name"`
	Description string `json:"err_description"`
	// RawMessage distinguishes an omitted field from explicit JSON null and
	// keeps future detail fields, numeric precision included.
	Details json.RawMessage `json:"err_details,omitempty"`
}

type Response struct {
	ResponseFormat int             `json:"response_format"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *Error          `json:"error,omitempty"`
}

// Resume is wired by pkg/api, as internal/adminapi's private Client adapters
// are. Keeping the bridge here avoids exposing a public caller-identity API or
// importing pkg/api from the protocol package.
var Resume func(Request) (json.RawMessage, error)

func object(raw json.RawMessage) bool {
	b := bytes.TrimSpace(raw)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}

// Decode accepts exactly one bounded JSON value. Unknown fields in a format-1
// request are refused; newer senders must still send this format's shape.
func decode(r io.Reader, value any) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil || len(b) > MaxBytes {
		return fmt.Errorf("resume delegation: unreadable or oversized message")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("resume delegation: malformed message: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("resume delegation: trailing message")
	}
	return nil
}

func (r Request) Validate() error {
	if r.RequestFormat != Format1 || r.ResponseFormat != Format1 {
		return fmt.Errorf("resume delegation: unsupported request or response format")
	}
	if !object(r.Params) || !filepath.IsAbs(r.Home) || !filepath.IsAbs(r.StorePath) || r.StoreID == "" ||
		(r.DefaultConfig && r.ConfigPath != "") || (!r.DefaultConfig && !filepath.IsAbs(r.ConfigPath)) ||
		r.Caller.PID <= 0 || r.Caller.Process == "" || r.Deadline.IsZero() {
		return fmt.Errorf("resume delegation: invalid request references")
	}
	for _, s := range []string{r.Home, r.StorePath, r.StoreID, r.ConfigPath, r.TmuxCommand, r.Caller.ParentInstanceID} {
		if strings.ContainsRune(s, 0) {
			return fmt.Errorf("resume delegation: invalid request string")
		}
	}
	return nil
}

func DecodeRequest(r io.Reader) (Request, error) {
	var request Request
	if err := decode(r, &request); err != nil {
		return request, err
	}
	return request, request.Validate()
}

func DecodeParams(raw json.RawMessage, value any) error {
	if !object(raw) {
		return fmt.Errorf("resume delegation: params must be an object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"instance_id", "send_mode", "confirmed_delivery", "confirm_from_transcripts"} {
		if b, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
			return fmt.Errorf("resume delegation: null parameter %s", name)
		}
	}
	return decode(bytes.NewReader(raw), value)
}

func DecodeResponse(r io.Reader, format int) (Response, error) {
	var response Response
	if err := decode(r, &response); err != nil {
		return response, err
	}
	if format != Format1 || response.ResponseFormat != format ||
		(response.Error == nil && !object(response.Result)) ||
		(response.Error != nil && (len(response.Result) != 0 || response.Error.Name == "" || response.Error.Description == "")) {
		return response, fmt.Errorf("resume delegation: invalid response shape or format")
	}
	if response.Error != nil && len(response.Error.Details) != 0 &&
		!object(response.Error.Details) && !bytes.Equal(bytes.TrimSpace(response.Error.Details), []byte("null")) {
		return response, fmt.Errorf("resume delegation: invalid error details")
	}
	return response, nil
}

func Encode(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > MaxBytes {
		return fmt.Errorf("resume delegation: oversized message")
	}
	_, err = w.Write(b)
	return err
}
