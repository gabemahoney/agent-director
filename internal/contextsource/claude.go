// Package contextsource reads bounded, structured context evidence. It never
// discovers agents, reads terminal output, or retains usage between calls.
package contextsource

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const ClaudeVersion = "2.1.280"

// ClaudeUsage is a single response, not the sum of a conversation's requests.
type ClaudeUsage struct {
	UUID, Model string
	Tokens      int64
	At          time.Time
	Counts      Counts
}

// Counts keeps cache categories distinct until the four categories are added.
type Counts struct {
	Input    *int64 `json:"input_tokens"`
	Creation *int64 `json:"cache_creation_input_tokens"`
	Read     *int64 `json:"cache_read_input_tokens"`
	Output   *int64 `json:"output_tokens"`
}

func (c Counts) Total() (int64, bool) {
	var total int64
	for _, n := range []*int64{c.Input, c.Creation, c.Read, c.Output} {
		if n == nil || *n < 0 || *n > math.MaxInt64-total {
			return 0, false
		}
		total += *n
	}
	return total, true
}

func (c Counts) Equal(other Counts) bool {
	if _, ok := c.Total(); !ok {
		return false
	}
	if _, ok := other.Total(); !ok {
		return false
	}
	return *c.Input == *other.Input && *c.Creation == *other.Creation && *c.Read == *other.Read && *c.Output == *other.Output
}

type claudeRecord struct {
	Type, Subtype, UUID string
	Parent              *string `json:"parentUuid"`
	Session             string  `json:"sessionId"`
	Agent               string  `json:"agentId"`
	Sidechain           bool    `json:"isSidechain"`
	Version, Timestamp  string
	Message             *struct {
		ID, Model string
		Usage     *struct {
			Counts
			Iterations []struct {
				Type string
				Counts
			} `json:"iterations"`
		} `json:"usage"`
	} `json:"message"`
}

// Claude reads the latest response on an unambiguous sequential active history.
// Branches and unknown structural records deliberately fail closed. Capacity
// callbacks bind the resulting UUID, so a later response requires new capacity
// provenance; a partial tail cannot fall back to an older response.
func Claude(session, child, path string) (ClaudeUsage, bool) {
	data, ok := readFile(path)
	if !ok || session == "" {
		return ClaudeUsage{}, false
	}
	var latest ClaudeUsage
	var previous string
	var previousMeasurement time.Time
	for _, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var r claudeRecord
		if len(line) == 0 || len(line) > 4<<20 || json.Unmarshal(line, &r) != nil {
			return ClaudeUsage{}, false
		}
		if r.Session != "" && r.Session != session {
			return ClaudeUsage{}, false
		}
		switch r.Type {
		case "mode", "permission-mode", "atis-latch", "file-history-snapshot", "last-prompt", "ai-title", "queue-operation", "cost-state", "progress":
			// These records have no active message-chain identity.
			continue
		case "summary":
			return ClaudeUsage{}, false
		case "user", "attachment", "system", "assistant":
		default:
			return ClaudeUsage{}, false
		}
		if r.Session != session || r.Agent != child || r.Sidechain != (child != "") || r.Version != ClaudeVersion || r.UUID == "" {
			return ClaudeUsage{}, false
		}
		if r.Type == "system" && r.Subtype == "compact_boundary" {
			latest = ClaudeUsage{}
			previousMeasurement = time.Time{}
			previous = r.UUID
			continue
		}
		if (previous == "" && r.Parent != nil && *r.Parent != "") || (previous != "" && (r.Parent == nil || *r.Parent != previous)) {
			return ClaudeUsage{}, false
		}
		previous = r.UUID
		if r.Type != "assistant" {
			continue
		}
		latest = ClaudeUsage{}
		if r.Message == nil || r.Message.ID == "" || r.Message.Model == "" || r.Message.Usage == nil {
			continue
		}
		counts := r.Message.Usage.Counts
		originalTotal, valid := counts.Total()
		if !valid {
			continue
		}
		// Claude's own 2.1.280 normalizer selects the last ordinary iteration,
		// excluding advisor and compaction subrequests. Never sum iterations.
		for i := len(r.Message.Usage.Iterations) - 1; i >= 0 && originalTotal > *counts.Output; i-- {
			it := r.Message.Usage.Iterations[i]
			if it.Type == "advisor_message" || it.Type == "compaction" {
				continue
			}
			if it.Type != "message" && it.Type != "fallback_message" {
				return ClaudeUsage{}, false
			}
			iterationTotal, valid := it.Counts.Total()
			if !valid {
				return ClaudeUsage{}, false
			}
			if iterationTotal > *it.Output {
				counts = it.Counts
			}
			break
		}
		total, valid := counts.Total()
		at, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		if !valid || err != nil || at.IsZero() || at.Year() < 0 || at.Year() > 9999 {
			continue
		}
		if at.Before(previousMeasurement) {
			return ClaudeUsage{}, false
		}
		previousMeasurement = at
		latest = ClaudeUsage{r.UUID, r.Message.Model, total, at.UTC(), counts}
	}
	return latest, latest.UUID != ""
}

func readFile(path string) ([]byte, bool) {
	const maximum = 64 << 20
	if !filepath.IsAbs(path) {
		return nil, false
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maximum {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || len(data) == 0 || len(data) > maximum || data[len(data)-1] != '\n' {
		return nil, false
	}
	after, err := f.Stat()
	if err != nil || int64(len(data)) != after.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, false
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(after, current) || current.Size() != after.Size() || !current.ModTime().Equal(after.ModTime()) {
		return nil, false
	}
	return data, true
}
