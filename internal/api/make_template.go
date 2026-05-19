package api

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/claude-director/internal/config"
)

// MakeTemplateParams is the typed parameter shape for the
// make-template verb. The Name + per-call spawn parameters subset is
// the SRD §10 surface; reserved per-invocation params (template,
// claude_instance_id, tmux_session_name) are intentionally absent.
type MakeTemplateParams struct {
	Name                 string
	CWD                  string
	RelayMode            string
	ClaudeArgs           []string
	ExtraEnv             map[string]string
	ClaudeDirectorLabels map[string]string
	Permissions          *MakeTemplatePermissions
}

// MakeTemplatePermissions is the params-side mirror of the on-disk
// permissions table. Held separately so a nil pointer means "no
// permissions baked in"; an empty struct means "explicit empty
// arrays". The merge path at spawn time distinguishes the two.
type MakeTemplatePermissions struct {
	Allow []string
	Deny  []string
	Ask   []string
}

// MakeTemplateResult is the typed return shape. Path is the absolute
// path of the written file, useful for tests and for CLI users who
// want to inspect the result.
type MakeTemplateResult struct {
	Path string `json:"path"`
}

// MakeTemplate writes a new template to ~/.claude-director/templates/.
// Behavior (SRD §10):
//
//   - Name safety: validated via config.ValidateTemplateName.
//   - RelayMode (when non-empty) must be "on" or "off".
//   - Templates dir is lazy-created (mode 0700) if missing.
//   - Overwrite is rejected with ErrTemplateExists via O_EXCL — the
//     final target is opened atomically, so a racing writer either
//     wins the create and we error, or we win and they error.
func MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	if err := config.ValidateTemplateName(params.Name); err != nil {
		return MakeTemplateResult{}, err
	}
	if err := validateTemplateRelayMode(params.RelayMode); err != nil {
		return MakeTemplateResult{}, err
	}

	if _, err := config.EnsureTemplatesDir(); err != nil {
		return MakeTemplateResult{}, err
	}

	target, err := config.TemplatePath(params.Name)
	if err != nil {
		return MakeTemplateResult{}, err
	}

	file := config.TemplateFile{
		CWD:                  params.CWD,
		RelayMode:            params.RelayMode,
		ClaudeArgs:           params.ClaudeArgs,
		ExtraEnv:             params.ExtraEnv,
		ClaudeDirectorLabels: params.ClaudeDirectorLabels,
	}
	if params.Permissions != nil {
		file.Permissions = &config.TemplatePermissions{
			Allow: params.Permissions.Allow,
			Deny:  params.Permissions.Deny,
			Ask:   params.Permissions.Ask,
		}
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(file); err != nil {
		return MakeTemplateResult{}, fmt.Errorf("api: encode template TOML: %w", err)
	}

	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return MakeTemplateResult{}, fmt.Errorf("%w: %s", config.ErrTemplateExists, target)
		}
		return MakeTemplateResult{}, fmt.Errorf("api: create template: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		_ = os.Remove(target)
		return MakeTemplateResult{}, fmt.Errorf("api: write template body: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(target)
		return MakeTemplateResult{}, fmt.Errorf("api: close template: %w", err)
	}

	return MakeTemplateResult{Path: filepath.Clean(target)}, nil
}

// validateTemplateRelayMode rejects an unrecognized relay_mode at write
// time so the file never lands with a value LoadTemplate would later
// reject as ErrTemplateMalformed.
func validateTemplateRelayMode(m string) error {
	switch m {
	case "", "on", "off":
		return nil
	}
	return fmt.Errorf("%w: relay_mode %q (want on/off)", config.ErrTemplateMalformed, m)
}
