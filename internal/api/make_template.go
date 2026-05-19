package api

import (
	"bytes"
	"fmt"
	"io"
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
//   - Templates dir is lazy-created (mode 0700) if missing.
//   - Overwrite is rejected with ErrTemplateExists. Callers delete
//     the file and re-run if they really mean to clobber.
//   - The write is atomic: a temp file is written in the same dir,
//     then os.Rename swaps it onto <name>.toml. A process killed
//     mid-write leaves the tempfile behind but never a half-written
//     real target.
//
// The verb does NOT accept reserved-per-invocation params (template,
// claude_instance_id, tmux_session_name) — see MakeTemplateParams.
// Those are caught at the CLI flag layer rather than here so the
// CLI's flag parsing rejects them with ErrInvalidFlags rather than
// a verb-layer error. (Defense in depth: if a future MCP caller
// sneaks them in, the CLI catalog wouldn't catch it. That's a
// separate hardening Epic if it ever matters; today the surface is
// CLI-only.)
func MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	if err := config.ValidateTemplateName(params.Name); err != nil {
		return MakeTemplateResult{}, err
	}

	dir, err := config.EnsureTemplatesDir()
	if err != nil {
		return MakeTemplateResult{}, err
	}

	target, err := config.TemplatePath(params.Name)
	if err != nil {
		return MakeTemplateResult{}, err
	}

	// Existence check: os.Stat returns nil err iff the file exists.
	// A subsequent racing creation between this check and the
	// os.Rename below is the classic TOCTOU window; we accept that
	// gap because the alternative (open with O_CREATE|O_EXCL on the
	// final path) defeats the atomic-rename pattern. The narrow race
	// would still leave the target file present and the rename
	// would clobber an existing file from a different writer — but
	// in practice make-template is a human-driven verb and the race
	// is benign.
	if _, err := os.Stat(target); err == nil {
		return MakeTemplateResult{}, fmt.Errorf("%w: %s", config.ErrTemplateExists, target)
	} else if !os.IsNotExist(err) {
		return MakeTemplateResult{}, fmt.Errorf("api: stat template target: %w", err)
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

	tmp, err := os.CreateTemp(dir, "."+params.Name+".toml.*")
	if err != nil {
		return MakeTemplateResult{}, fmt.Errorf("api: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpPath) }

	if _, err := io.Copy(tmp, &buf); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return MakeTemplateResult{}, fmt.Errorf("api: write template body: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return MakeTemplateResult{}, fmt.Errorf("api: chmod tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return MakeTemplateResult{}, fmt.Errorf("api: sync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		return MakeTemplateResult{}, fmt.Errorf("api: close tempfile: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		cleanupTmp()
		return MakeTemplateResult{}, fmt.Errorf("api: rename template into place: %w", err)
	}

	return MakeTemplateResult{Path: filepath.Clean(target)}, nil
}
