package qualification

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type ForceRow struct {
	State   string `json:"state"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}
type Contract struct {
	SHA256 string     `json:"sha256"`
	Force  []ForceRow `json:"force"`
	Source string     `json:"source"`
}

// ParseContract reads the exact packaged Markdown table, rather than keeping
// another normative expectation table in the qualification harness.
func ParseContract(doc []byte) (Contract, error) {
	c := Contract{SHA256: runner.Digest(doc), Source: "docs/caller-contract.md#claude-code-force-capability"}
	s := string(doc)
	start := strings.Index(s, "#### Claude Code force capability\n")
	if start < 0 {
		return c, errors.New("candidate force contract missing")
	}
	s = s[start:]
	end := strings.Index(s, "#### Sighting limits\n")
	if end < 0 {
		return c, errors.New("candidate sighting limits missing")
	}
	s = s[:end]
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		cols := strings.Split(line, "|")
		if len(cols) != 5 {
			continue
		}
		for i := range cols {
			cols[i] = strings.Trim(strings.TrimSpace(cols[i]), "`")
		}
		if cols[2] != "pending" && cols[2] != "handed_over" && cols[2] != "refused" {
			continue
		}
		if seen[cols[1]] {
			return c, errors.New("duplicate force contract state")
		}
		seen[cols[1]] = true
		c.Force = append(c.Force, ForceRow{cols[1], cols[2], cols[3]})
	}
	if len(c.Force) == 0 {
		return c, errors.New("empty force contract")
	}
	return c, nil
}

func CheckContractAgreement(doc []byte, runtime json.RawMessage) runner.Result {
	c, err := ParseContract(doc)
	if err != nil {
		return result("contract", "not_completed", err.Error(), "candidate docs/runtime agreement", nil)
	}
	var actual struct {
		Force struct {
			States []ForceRow `json:"states"`
		} `json:"force"`
	}
	if err = json.Unmarshal(runtime, &actual); err != nil || len(actual.Force.States) == 0 {
		return result("contract", "not_completed", "runtime capability table missing", "get.delivery_capabilities.force.states", nil)
	}
	exported := store.BusySendCapabilitiesFor("claude")
	var compiled []ForceRow
	for _, r := range exported.ForceStates {
		compiled = append(compiled, ForceRow{string(r.State), string(r.Outcome), string(r.Reason)})
	}
	status := "pass"
	reason := "candidate docs, compiled capabilities and runtime agree"
	if !reflect.DeepEqual(c.Force, actual.Force.States) || !reflect.DeepEqual(c.Force, compiled) {
		status = "fail"
		reason = "candidate capability drift"
	}
	// Force rows alone cannot hide changed runtime admission limits or
	// confirmation capabilities. Compare every published capability with the
	// candidate's one compiled table, then check the documented numeric prose.
	var capabilities api.DeliveryCapabilities
	if err := json.Unmarshal(runtime, &capabilities); err != nil {
		return result("contract", "not_completed", err.Error(), "complete runtime capabilities", nil)
	}
	limitsAgree := len(capabilities.Modes) == len(exported.Modes) && capabilities.HeldCount == exported.HeldCount && capabilities.HeldBytes == exported.HeldBytes && capabilities.ConfirmFromTranscriptsCapability == string(exported.ConfirmFromTranscripts) && capabilities.ConfirmFromTranscriptsReason == string(exported.ConfirmFromTranscriptsReason) && capabilities.Force != nil
	if capabilities.Force != nil {
		limitsAgree = limitsAgree && capabilities.Force.Admission == string(exported.ForceAdmission) && len(capabilities.Force.Limits) == len(exported.ForceLimits)
		for i, limit := range exported.ForceLimits {
			if i >= len(capabilities.Force.Limits) || capabilities.Force.Limits[i] != string(limit) || !strings.Contains(string(doc), "`"+string(limit)+"`") {
				limitsAgree = false
			}
		}
	}
	text := strings.Join(strings.Fields(string(doc)), " ")
	numbers := regexp.MustCompile(`each allowing ([0-9,]+) Message bytes and ([0-9,]+) prompt bytes`).FindStringSubmatch(text)
	held := regexp.MustCompile(`held limits are ([0-9,]+) Messages and ([0-9,]+) bytes`).FindStringSubmatch(text)
	readNumber := func(s string) int64 {
		n, err := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
		if err != nil {
			return -1
		}
		return n
	}
	if len(numbers) != 3 || len(held) != 3 {
		return result("contract", "not_completed", "documented Claude numeric limits could not be parsed", "candidate documented limits", nil)
	}
	limitsAgree = limitsAgree && readNumber(held[1]) == exported.HeldCount && readNumber(held[2]) == exported.HeldBytes && strings.Contains(string(doc), "## Busy-send coverage\n") && strings.Contains(string(doc), "#### Sighting limits\n")
	for i, mode := range exported.Modes {
		if i >= len(capabilities.Modes) {
			limitsAgree = false
			continue
		}
		got := capabilities.Modes[i]
		limitsAgree = limitsAgree && got.SendMode == string(mode.Mode) && got.MessageBytes == mode.MessageBytes && got.PromptBytes == mode.PromptBytes && got.TextKind == string(mode.TextKind) && readNumber(numbers[1]) == mode.MessageBytes && readNumber(numbers[2]) == mode.PromptBytes && strings.Contains(string(doc), "`"+string(mode.Mode)+"`")
	}
	if !limitsAgree {
		status, reason = "fail", "candidate docs, compiled admission/confirmation capabilities and runtime disagree"
	}
	return result("contract", status, reason, c.Force, actual.Force.States, "raw/caller-contract.md", "raw/capabilities.json")
}

func (c Contract) Row(state string) (ForceRow, error) {
	for _, r := range c.Force {
		if r.State == state {
			return r, nil
		}
	}
	return ForceRow{}, fmt.Errorf("unknown documented force state %q", state)
}
