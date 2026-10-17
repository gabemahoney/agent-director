package apitest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TemplateFields are the fields of a spawn template body (SRD §10.2), the
// same ones make-template writes. A zero field is left out of the body, as
// make-template leaves it out. To add a template field, add it here and copy it
// in TemplateBody's config.TemplateFile literal.
type TemplateFields struct {
	Agent       string                      // agent ("claude", "codex" or "cursor"; any other non-empty text builds a body the loader refuses)
	AgentArgs   []string                    // agent_args
	CWD         string                      // cwd
	RelayMode   string                      // relay_mode ("on" or "off"; any other text builds a body the loader refuses)
	ExtraEnv    map[string]string           // [extra_env]
	Labels      map[string]string           // [labels]
	Permissions *config.TemplatePermissions // [permissions] allow, deny and ask

	// RawKeys are written verbatim, one per line, ahead of the encoded fields,
	// for hand-edited shapes the encoder cannot produce: an old key
	// (`claude_args = ["--x"]`), both an old and a new name, a mis-cased key, a
	// duplicate key. Each entry must be one top-level `key = value` line (a
	// table header would capture the encoded top-level keys that follow it).
	RawKeys []string
}

// TemplateBody builds a template body for SeedTemplate from f. The fields are
// encoded exactly as make-template encodes them (the BurntSushi encoder over
// config.TemplateFile), so a body built here is the file make-template would
// write for the same fields; the raw keys come first, exactly as given. The
// body loads through config.LoadTemplate when it holds no raw keys that the
// loader refuses.
//
//	body := apitest.TemplateBody(apitest.TemplateFields{
//		AgentArgs: []string{"--model", "x"},
//		CWD:       "/tmp",
//		RawKeys:   []string{`claude_args = ["--old"]`},
//	})
//	path, err := apitest.SeedTemplate(config.TemplatesDir(), "name", body)
//
// It panics when the encoder fails, which only a broken field can cause.
func TemplateBody(f TemplateFields) string {
	file := config.TemplateFile{
		CWD:                 f.CWD,
		RelayMode:           f.RelayMode,
		Agent:               f.Agent,
		AgentArgs:           f.AgentArgs,
		ExtraEnv:            f.ExtraEnv,
		AgentDirectorLabels: f.Labels,
		Permissions:         f.Permissions,
	}
	var buf bytes.Buffer
	for _, line := range f.RawKeys {
		buf.WriteString(strings.TrimRight(line, "\n"))
		buf.WriteByte('\n')
	}
	if err := toml.NewEncoder(&buf).Encode(file); err != nil {
		panic(fmt.Sprintf("apitest: TemplateBody: encode: %v", err))
	}
	return buf.String()
}
