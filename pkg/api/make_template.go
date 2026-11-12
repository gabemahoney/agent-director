package api

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
)

// MakeTemplateParams is the typed parameter shape for the
// make-template verb. The Name + per-call spawn parameters subset is
// the SRD §10 surface; reserved per-invocation params (template,
// instance_id, tmux_session_name, reuse_finished) are intentionally
// absent. reuse_finished applies to one spawn call only, so no template
// records it.
type MakeTemplateParams struct {
	// Name is the template filename (without extension). Must be filename-safe:
	// no path separators, no leading dot, no "..". Required.
	Name string
	// CWD is an optional default working directory to bake into the template.
	// Per-call cwd overrides at spawn time.
	CWD string
	// Agent is the template's agent (case-sensitive), or ""
	// for none: no agent key is stored then. It is the agent the template's
	// agent_args belong to and the agent a spawn that names none takes (SR-1.2,
	// SR-2.1). An unrecognized value is refused with ErrTemplateMalformed.
	Agent string
	// RelayMode is an optional default relay mode: "on", "off", or "". Empty
	// leaves spawn to use off for pane-only agents or the config default for
	// others. Pane-only templates refuse on. Per-call relay_mode overrides.
	RelayMode string
	// AgentArgs is an optional default argv passed through to the agent program. Per-call
	// agent_args REPLACES the template array wholesale (not concatenated).
	AgentArgs []string
	// ExtraEnv is an optional map of env-var overrides to bake in. Per-call
	// extra_env entries merge by key; per-call wins on collision. A key
	// spawn would refuse is refused here, by spawn's own check
	// (spawn.ValidateExtraEnv), with ErrReservedEnvKey, and nothing is
	// written: a reserved key (AGENT_DIRECTOR_*, or HOME with any value), or
	// a key that is not a valid env-var name (empty, or holding '=' or a NUL
	// byte; the description says which). To give the agent its own config,
	// set an absolute CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex).
	ExtraEnv map[string]string
	// AgentDirectorLabels is an optional map of label k=v pairs to bake in.
	// Per-call label entries merge by key; per-call wins on collision.
	AgentDirectorLabels map[string]string
	// Permissions is an optional permission overlay. Nil means no permissions
	// baked in; a non-nil value with empty arrays serializes as explicit [].
	// Per-call allow/deny/ask entries CONCATENATE with the template's arrays.
	Permissions *MakeTemplatePermissions
	// Overwrite selects the write algorithm at the final file-write step.
	// When true, the encoded TOML body is written via an atomic
	// sibling-tempfile algorithm: os.CreateTemp in the templates directory,
	// write, close, then os.Rename(2) over the target path — replacing any
	// existing file. When false (the Go zero value), the existing
	// O_EXCL create-only path is used and a pre-existing target yields
	// ErrTemplateExists. All precondition checks (name safety, relay-mode
	// validation, extra_env validation, templates-dir creation, TOML encode)
	// run identically on both branches; Overwrite is consulted only at the
	// write step, so a refused template never replaces an existing one.
	Overwrite bool
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
	// Nondeterministic: reflects ~/.config/agent-director/templates/ on the host.
	Path string `json:"path"`
}

// MakeTemplate writes a new template to ~/.config/agent-director/templates/.
// Behavior (SRD §10):
//
//   - The effective config's experimental flag must enable a pane-only agent
//     (CheckExperimental), before even the name check.
//   - Name safety: validated via config.ValidateTemplateName.
//   - RelayMode (when non-empty) must be "on" or "off".
//   - Agent (when non-empty) must name a recognized agent, else
//     ErrTemplateMalformed; a non-empty AgentArgs with no Agent is
//     ErrTemplateAgentMissing. Pane-only templates then use
//     ValidatePaneOnlySettings with the model optional: these agents accept only
//     one split --model ID when supplied, relay_mode on is
//     ErrAgentCapabilityUnsupported, and any allow, deny or ask entry is
//     ErrPermissionsUnsupported. Otherwise Cursor arguments are validated
//     with ErrSpawnDeniedFlag on refusal, and codex/cursor permission lists
//     are refused with ErrPermissionsUnsupported (SR-1.4). These agent checks
//     run after RelayMode and before ExtraEnv.
//   - ExtraEnv: spawn's whole extra_env check (spawn.ValidateExtraEnv, bug
//     b.66q), in spawn's order: the effective pane-only agent's forbidden
//     keys, then HOME, the AGENT_DIRECTOR_ prefix and malformed keys, then
//     Cursor's credential/directory checks for Cursor. All return
//     ErrReservedEnvKey, without values in pane-only diagnostics. The key is refused,
//     never rewritten, so no template is saved that every spawn using it
//     would refuse.
//   - These checks run before the templates dir is created and before any
//     write, so a refused call creates, writes and replaces nothing,
//     whatever Overwrite is.
//   - Templates dir is lazy-created (mode 0700) if missing.
//   - Write step branches on params.Overwrite:
//   - Overwrite == true: the encoded body is written to a sibling
//     tempfile via os.CreateTemp, closed, then os.Rename'd over the
//     target path. The rename is atomic on POSIX, so a pre-existing
//     template is replaced in place and no half-written file is ever
//     observable at the target path. Any failure between CreateTemp
//     and Rename removes the tempfile before returning.
//   - Overwrite == false (zero value): the target is opened with
//     O_WRONLY|O_CREATE|O_EXCL; a pre-existing target yields
//     ErrTemplateExists with the absolute target path embedded.
func MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	cfg := config.Default()
	if spawn.IsPaneOnly(params.Agent) {
		var err error
		cfg, err = config.Load(defaultConfigPath)
		if err != nil {
			return MakeTemplateResult{}, err
		}
	}
	return makeTemplate(params, cfg)
}

func makeTemplate(params MakeTemplateParams, cfg config.Config) (MakeTemplateResult, error) {
	if err := spawn.CheckExperimental(params.Agent, cfg.Experimental); err != nil {
		return MakeTemplateResult{}, err
	}
	if err := config.ValidateTemplateName(params.Name); err != nil {
		return MakeTemplateResult{}, err
	}
	if err := validateTemplateRelayMode(params.RelayMode); err != nil {
		return MakeTemplateResult{}, err
	}
	if err := validateTemplateAgent(params, cfg.Experimental); err != nil {
		return MakeTemplateResult{}, err
	}
	if err := spawn.ValidateExtraEnv(params.ExtraEnv, params.Agent); err != nil {
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
		CWD:                 params.CWD,
		RelayMode:           params.RelayMode,
		Agent:               params.Agent,
		AgentArgs:           params.AgentArgs,
		ExtraEnv:            params.ExtraEnv,
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

	if params.Overwrite {
		templatesDir := filepath.Dir(target)
		tmp, err := os.CreateTemp(templatesDir, "."+params.Name+".toml.tmp.*")
		if err != nil {
			return MakeTemplateResult{}, fmt.Errorf("api: create template tempfile: %w", err)
		}
		tempPath := tmp.Name()
		cleanup := func() {
			if rmErr := os.Remove(tempPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				// best-effort cleanup; ignore non-ENOENT errors
				_ = rmErr
			}
		}
		if _, err := tmp.Write(buf.Bytes()); err != nil {
			_ = tmp.Close()
			cleanup()
			return MakeTemplateResult{}, fmt.Errorf("api: write template body: %w", err)
		}
		if err := tmp.Close(); err != nil {
			cleanup()
			return MakeTemplateResult{}, fmt.Errorf("api: close template: %w", err)
		}
		if err := os.Rename(tempPath, target); err != nil {
			cleanup()
			return MakeTemplateResult{}, fmt.Errorf("api: rename template into place: %w", err)
		}
		return MakeTemplateResult{Path: filepath.Clean(target)}, nil
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

// validateTemplateAgent is make-template's agent checks, in SR-1.4's order
// (steps 3 to 5): a value other than claude, codex, cursor, droid or opencode is ErrTemplateMalformed,
// as a bad relay_mode is; a non-empty AgentArgs with no Agent is
// ErrTemplateAgentMissing. A pane-only template then uses
// spawn.ValidatePaneOnlySettings with the model optional, refusing unsupported
// arguments, relay_mode on and permission lists in that order. Droid's optional
// model must be one split --model ID; permission lists give ErrPermissionsUnsupported.
// For other agents, Cursor argument validation follows, then codex/cursor
// permission lists give ErrPermissionsUnsupported (spawn.RefuseCodexPermissions
// or spawn.RefuseCursorPermissions).
// All run before the templates directory is created, so a refused call
// writes nothing.
func validateTemplateAgent(params MakeTemplateParams, experimental config.Experimental) error {
	if params.Agent != "" && !config.ValidAgent(params.Agent) {
		return fmt.Errorf("%w: agent %q (want %s)", config.ErrTemplateMalformed,
			params.Agent, config.AgentChoices(experimental))
	}
	if params.Agent == "" && len(params.AgentArgs) > 0 {
		return fmt.Errorf("%w: agent_args are set but no agent is given; pass agent %s, or remove agent_args",
			config.ErrTemplateAgentMissing, config.AgentChoices(experimental))
	}
	if spawn.IsPaneOnly(params.Agent) {
		var permissions *spawn.Permissions
		if params.Permissions != nil {
			permissions = &spawn.Permissions{Allow: params.Permissions.Allow, Deny: params.Permissions.Deny, Ask: params.Permissions.Ask}
		}
		return spawn.ValidatePaneOnlySettings(params.Agent, params.AgentArgs, params.RelayMode, permissions, false)
	}
	if params.Agent == config.AgentCursor {
		if _, _, err := spawn.CursorArgs(params.AgentArgs); err != nil {
			return err
		}
		if params.Permissions != nil {
			return spawn.RefuseCursorPermissions(params.Permissions.Allow, params.Permissions.Deny, params.Permissions.Ask)
		}
	}
	if params.Agent == config.AgentCodex && params.Permissions != nil {
		return spawn.RefuseCodexPermissions(params.Permissions.Allow, params.Permissions.Deny, params.Permissions.Ask)
	}
	return nil
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
// ~/.config/agent-director/templates/<name>.toml. The file is created exclusively
// (O_EXCL); overwriting an existing template returns [ErrTemplateExists].
// Use the template name with SpawnParams.Template to apply it at spawn time.
//
// CLI: agent-director make-template
//
// Errors:
//   - ErrTemplateNameUnsafe: Name contains path-unsafe characters (path
//     separators, leading dot, or "..").
//   - ErrTemplateExists: a template with that name already exists.
//   - ErrTemplateMalformed: RelayMode is not "on", "off", or "", or Agent
//     is non-empty and unrecognized (the message quotes the value).
//   - ErrTemplateAgentMissing: AgentArgs is non-empty and Agent is empty;
//     nothing is written. Give the template's agent or drop AgentArgs.
//   - ErrExperimentalAgentDisabled: the template's experimental agent is
//     disabled; enable its [experimental] flag before creating the template.
//   - ErrAgentCapabilityUnsupported: a pane-only template requests RelayMode
//     "on"; omit that capability. Nothing is written.
//   - ErrSpawnDeniedFlag: a Cursor template has unsupported or malformed
//     agent arguments. Remove the named argument and any owned value, then
//     retry. Pane-only agents accept no arguments or one split --model with a
//     nonempty model ID in the form the agent requires: a subscription or
//     provider-qualified ID. Malformed or duplicate model options, --model=ID,
//     custom-model IDs, a wrong or missing provider and every other argument
//     (including task text) are refused without echoing rejected values.
//     A pane-only template may omit the model; the resolved spawn must supply it.
//     Nothing is written.
//   - ErrPermissionsUnsupported: Agent is "codex", "cursor" or pane-only and
//     Permissions holds an allow, deny or ask entry. Empty lists are allowed;
//     no template is written on refusal.
//   - ErrReservedEnvKey: ExtraEnv contains a key whose name before the
//     first '=' (the name tmux sets) is reserved: it starts with
//     AGENT_DIRECTOR_, or it is HOME (the key HOME, or one such as
//     "HOME=/x") with any value, an empty one included. Also returned for a
//     malformed key: empty, or containing '=' or a NUL byte. Such keys are
//     refused, never rewritten; a reserved and malformed key gets the
//     reserved-name refusal. For Claude/Codex/Cursor or an agent-independent
//     template, these messages quote the key. Claude can use absolute
//     CLAUDE_CONFIG_DIR and Codex absolute CODEX_HOME for its own config;
//     the HOME refusal names both when Agent is empty. Cursor uses absolute
//     CURSOR_CONFIG_DIR and CURSOR_DATA_DIR with its existing personal login,
//     without account isolation. Effective Cursor templates also refuse
//     explicit credential-profile selectors and invalid directory paths.
//     Pane-only templates additionally refuse their credential, configuration and home
//     selectors, even with empty values. The name before the first '=' is checked, so malformed variants
//     cannot bypass the refusal. Pane-only agents keep the invoking HOME. Pane-only diagnostics omit values and raw
//     malformed key text. Agent-independent templates defer agent-specific
//     environment checks until resolution. Nothing is written or replaced.
//
// Nondeterminism: .path — the absolute path reflects the host's templates
// directory (~/.config/agent-director/templates/) and varies across environments.
func (c *Client) MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	if err := c.checkClosed(); err != nil {
		return MakeTemplateResult{}, err
	}
	return makeTemplate(params, c.cfg)
}
