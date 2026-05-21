package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// LoadTemplate reads ~/.agent-director/templates/<name>.toml and
// decodes it into a TemplateFile.
//
// Behavior (SRD §10.2):
//
//   - Name safety is enforced via ValidateTemplateName + TemplatePath.
//   - Missing file → ErrTemplateNotFound.
//   - Decode failure → ErrTemplateMalformed.
//   - Unknown top-level keys → ErrTemplateMalformed (the BurntSushi
//     decoder accepts unknown keys by default; we walk the
//     MetaData.Undecoded() result and reject if any survive).
//   - relay_mode validation: must be "" / "on" / "off" — anything else
//     is ErrTemplateMalformed at load time, before merge.
//
// LoadTemplate is read-only; it never creates or modifies the
// templates dir. The merge step (spawn.Resolve) consumes the returned
// TemplateFile and folds it into the per-call params per SRD §7.1.
func LoadTemplate(name string) (TemplateFile, error) {
	path, err := TemplatePath(name)
	if err != nil {
		return TemplateFile{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TemplateFile{}, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
		}
		return TemplateFile{}, fmt.Errorf("config: read template %s: %w", name, err)
	}

	var tf TemplateFile
	meta, err := toml.Decode(string(data), &tf)
	if err != nil {
		return TemplateFile{}, fmt.Errorf("%w: %s: %v", ErrTemplateMalformed, name, err)
	}

	// Reject unknown top-level keys. BurntSushi reports them via
	// meta.Undecoded(); a non-empty slice means the file carries
	// fields our struct doesn't know about, which is almost certainly
	// a typo or a forward-compat mismatch we want to surface loudly.
	if extras := meta.Undecoded(); len(extras) > 0 {
		return TemplateFile{}, fmt.Errorf("%w: %s: unknown key(s): %v",
			ErrTemplateMalformed, name, extras)
	}

	if tf.RelayMode != "" && tf.RelayMode != "on" && tf.RelayMode != "off" {
		return TemplateFile{}, fmt.Errorf("%w: %s: relay_mode=%q (want on/off)",
			ErrTemplateMalformed, name, tf.RelayMode)
	}

	return tf, nil
}
