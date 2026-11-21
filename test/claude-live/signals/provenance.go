package signals

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func (c capture) indexed(name string) ([]byte, error) {
	if _, ok := c.m.Artifacts[name]; !ok {
		return nil, fmt.Errorf("unindexed required evidence %s", name)
	}
	return runner.Read(c.root, name)
}

func hexadecimal(value string, size int) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == size && value == strings.ToLower(value)
}

func (c *capture) provenance() error {
	b, err := c.indexed("raw/candidate-installed.json")
	if err != nil {
		return err
	}
	var proof runner.CandidateEvidence
	if json.Unmarshal(b, &proof) != nil {
		return errors.New("invalid candidate provenance")
	}
	k := proof.Kit
	if proof.Schema != runner.Schema || proof.RunID != c.m.RunID || !proof.Installed || k.Schema != runner.Schema || !hexadecimal(k.BaseCommit, 20) || !hexadecimal(k.SnapshotTree, 20) || !hexadecimal(k.SourceSHA256, 32) || !hexadecimal(k.DiffSHA256, 32) || k.CommitStamp != k.BaseCommit || k.Version == "" {
		return errors.New("incomplete candidate provenance")
	}
	if c.m.Candidate != k.BaseCommit+"/"+k.SnapshotTree || !hexadecimal(proof.KitSHA256, 32) || c.m.KitSHA256 != proof.KitSHA256 || c.m.Platform != k.Platform || (k.Platform != "linux/amd64" && k.Platform != "linux/arm64") || c.m.ProviderVersion != proof.ProviderVersion || proof.ProviderVersion == "" {
		return errors.New("manifest candidate, kit, platform or provider differs from raw provenance")
	}
	arch := strings.TrimPrefix(k.Platform, "linux/")
	for _, name := range []string{"bin/claude-live", "payload/agent-director-linux-" + arch, "payload/agent-director-admin-linux-" + arch} {
		a, ok := k.Files[name]
		if !ok || !hexadecimal(a.SHA256, 32) || a.Bytes <= 0 {
			return errors.New("missing candidate executable provenance")
		}
	}
	if c.m.RecordingHook.SHA256 != k.Files["bin/claude-live"].SHA256 {
		return errors.New("recorder identity differs from verified kit")
	}
	b, err = c.indexed("commands.jsonl")
	if err != nil {
		return err
	}
	c.commands = map[string]runner.Command{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var cmd runner.Command
		if json.Unmarshal([]byte(line), &cmd) != nil || cmd.Stdout == "" {
			return errors.New("invalid command evidence")
		}
		if _, exists := c.commands[cmd.Stdout]; exists {
			return errors.New("duplicate command artifact reference")
		}
		if _, err := c.indexed(cmd.Stdout); err != nil {
			return err
		}
		if cmd.Stderr != "" {
			if _, err := c.indexed(cmd.Stderr); err != nil {
				return err
			}
		}
		c.commands[cmd.Stdout] = cmd
	}
	checks := []struct {
		ref, name string
		argv      []string
	}{
		{proof.ProviderCommand, "claude-version", []string{"claude", "--version"}},
		{proof.RuntimeCommand, "verify-agent-director", []string{runner.TestHome + "/.config/agent-director/bin/agent-director", "version"}},
		{proof.AdminCommand, "verify-agent-director-admin", []string{runner.TestHome + "/.config/agent-director/admin/agent-director-admin", "version"}},
	}
	for i, check := range checks {
		cmd, ok := c.commands[check.ref]
		if !ok || cmd.Name != check.name || !reflect.DeepEqual(cmd.Argv, check.argv) || cmd.ExitCode != 0 || cmd.Timeout || cmd.SpawnError != "" || cmd.Started.IsZero() || cmd.Ended.Before(cmd.Started) || cmd.Started.Before(c.m.Started) || cmd.Ended.After(c.m.Finished) {
			return fmt.Errorf("missing successful identity command %s", check.name)
		}
		b, err := c.indexed(check.ref)
		if err != nil {
			return err
		}
		if i == 0 {
			if strings.TrimSpace(string(b)) != proof.ProviderVersion {
				return errors.New("provider version differs from command output")
			}
		} else {
			var v struct {
				Version string `json:"version"`
				Commit  string `json:"commit"`
			}
			if json.Unmarshal(b, &v) != nil || v.Version != k.Version || v.Commit != k.CommitStamp {
				return errors.New("candidate version differs from installed-pair command output")
			}
		}
	}
	return nil
}

// CandidateCommands reuses the signal capture's exact installed-pair, kit,
// recorder and successful identity-command validation for qualification.
// The caller supplies a manifest already checked by runner.ReadManifest.
func CandidateCommands(root string, m runner.Manifest) (map[string]runner.Command, error) {
	c := capture{root: root, m: m}
	if err := c.provenance(); err != nil {
		return nil, err
	}
	return c.commands, nil
}
