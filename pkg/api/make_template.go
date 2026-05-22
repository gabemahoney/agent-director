package api

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/config"
)

// MakeTemplateParams is the typed parameter shape for the
// make-template verb. The Name + per-call spawn parameters subset is
// the SRD §10 surface; reserved per-invocation params (template,
// claude_instance_id, tmux_session_name) are intentionally absent.
type MakeTemplateParams struct {
	// Name is the template filename (without extension). Must be filename-safe:
	// no path separators, no leading dot, no "..". Required.
	Name string
	// CWD is an optional default working directory to bake into the template.
	// Per-call --cwd overrides at spawn time.
	CWD string
	// RelayMode is an optional default relay mode: "on", "off", or "" (inherit
	// config default). Per-call --relay-mode overrides at spawn time.
	RelayMode string
	// ClaudeArgs is an optional default argv passed through to claude. Per-call
	// --claude-args REPLACES the template array wholesale (not concatenated).
	ClaudeArgs []string
	// ExtraEnv is an optional map of env-var overrides to bake in. Per-call
	// --extra-env entries merge by key; per-call wins on collision.
	ExtraEnv map[string]string
	// AgentDirectorLabels is an optional map of label k=v pairs to bake in.
	// Per-call --label entries merge by key; per-call wins on collision.
	AgentDirectorLabels map[string]string
	// Permissions is an optional permission overlay. Nil means no permissions
	// baked in; a non-nil value with empty arrays serializes as explicit [].
	// Per-call --allow/--deny/--ask entries CONCATENATE with the template's arrays.
	Permissions *MakeTemplatePermissions
}

// MakeTemplatePermissions is the params-side mirror of the on-disk
// permissions table. Held separately so a nil pointer means "no
// permissions baked in"; an empty struct means "explicit empty
// arrays". The merge path at spawn time distinguishes the two.
type MakeTemplatePermissions struct {
	// Allow is the list of permission patterns to allow at spawn time.
	Allow []string
	// Deny is the list of permission patterns to deny at spawn time.
	Deny []string
	// Ask is the list of permission patterns to ask about at spawn time.
	Ask []string
}

// MakeTemplateResult is the typed return shape. Path is the absolute
// path of the written file, useful for tests and for CLI users who
// want to inspect the result.
type MakeTemplateResult struct {
	// Path is the absolute path of the written template TOML file.
	// Nondeterministic: reflects ~/.agent-director/templates/ on the host.
	Path string `json:"path"`
}

// MakeTemplate writes a new template to ~/.agent-director/templates/.
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
		AgentDirectorLabels: params.AgentDirectorLabels,
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

// MakeTemplate writes a reusable spawn preset to
// ~/.agent-director/templates/<name>.toml. The file is created exclusively
// (O_EXCL); overwriting an existing template returns [ErrTemplateExists].
// Use the template name with SpawnParams.Template to apply it at spawn time.
//
// CLI: agent-director make-template
//
// Errors:
//   - ErrTemplateNameUnsafe: Name contains path-unsafe characters (path
//     separators, leading dot, or "..").
//   - ErrTemplateExists: a template with that name already exists.
//   - ErrTemplateMalformed: RelayMode is not "on", "off", or "".
//
// Nondeterminism: .path — the absolute path reflects the host's templates
// directory (~/.agent-director/templates/) and varies across environments.
func (c *Client) MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	if err := c.checkClosed(); err != nil {
		return MakeTemplateResult{}, err
	}
	return MakeTemplate(params)
}
