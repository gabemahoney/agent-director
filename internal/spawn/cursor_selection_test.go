package spawn

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCursorSelectionValidation(t *testing.T) {
	for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR"} {
		for _, value := range []string{"", "relative", "~/private", "$ROOT/private", "/tmp/../private", "/tmp//private", "/tmp/private/", "/tmp/a\nb", "/tmp/a\x00b", "/tmp/a\x7fb", "/", "/literal $ROOT/~name/多行"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				env := map[string]string{key: value}
				before := maps.Clone(env)
				r := Resolved{SpawnParams: SpawnParams{Agent: "cursor", CWD: t.TempDir(), ExtraEnv: env}}
				err := Validate(&r)
				valid := value == "/" || value == "/literal $ROOT/~name/多行"
				if valid && err != nil || !valid && !errors.Is(err, ErrReservedEnvKey) {
					t.Fatalf("Validate = %v; accepted=%t", err, valid)
				}
				if err != nil && !strings.Contains(err.Error(), key) {
					t.Fatalf("missing selector name: %v", err)
				}
				if !reflect.DeepEqual(env, before) {
					t.Fatal("selection mutated")
				}
			})
		}
	}
	path := filepath.Join(t.TempDir(), "not-created")
	r := Resolved{SpawnParams: SpawnParams{Agent: "cursor", CWD: t.TempDir(), ExtraEnv: map[string]string{"CURSOR_CONFIG_DIR": path}}}
	if err := Validate(&r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("validation created directory: %v", err)
	}
}

func TestCursorProfileRefusal(t *testing.T) {
	for _, key := range []string{"CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
		for _, value := range []string{"", "memory", "synthetic-secret-do-not-print"} {
			for _, agent := range []string{"cursor", "claude", "codex"} {
				t.Run(agent+"/"+key+"/"+value, func(t *testing.T) {
					r := Resolved{SpawnParams: SpawnParams{Agent: agent, CWD: t.TempDir(), ExtraEnv: map[string]string{key: value}}}
					err := Validate(&r)
					if agent != "cursor" {
						if err != nil {
							t.Fatal(err)
						}
						return
					}
					if !errors.Is(err, ErrReservedEnvKey) || !strings.Contains(err.Error(), key) {
						t.Fatalf("profile accepted or wrong error: %v", err)
					}
					if value != "" && strings.Contains(err.Error(), value) {
						t.Fatalf("profile value in error: %v", err)
					}
				})
			}
		}
	}
}

func TestCursorSelectionCheckPrecedence(t *testing.T) {
	for _, key := range []string{"HOME", "AGENT_DIRECTOR_GROUP", "A=B"} {
		r := Resolved{SpawnParams: SpawnParams{Agent: "cursor", CWD: t.TempDir(), ExtraEnv: map[string]string{key: "x", "CURSOR_API_KEY": "synthetic-secret"}}}
		err := Validate(&r)
		if !errors.Is(err, ErrReservedEnvKey) || !strings.Contains(err.Error(), `"`+key+`"`) || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("precedence: %v", err)
		}
	}
}

func TestCursorSelectionComposition(t *testing.T) {
	e := newLaunchEnv(t)
	installCompositionGateway(t)
	e.r.Agent = "cursor"
	e.r.ExtraEnv = map[string]string{"CURSOR_CONFIG_DIR": "/explicit/config with spaces", "CURSOR_DATA_DIR": "/explicit/data", "OTHER": "retained"}
	c, err := ComposeLaunch(e.r, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range e.r.ExtraEnv {
		if c.Create.Env[key] != value {
			t.Fatalf("transport lost %s", key)
		}
	}
	e.r.ExtraEnv["CURSOR_API_KEY"] = "synthetic-secret"
	if _, err := ComposeLaunch(e.r, e.cfg); !errors.Is(err, ErrReservedEnvKey) || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("composition profile: %v", err)
	}
}
