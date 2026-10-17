package cursorhost

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestInitializeCapabilityShapes(t *testing.T) {
	for _, tc := range []struct {
		name, raw   string
		valid, load bool
	}{
		{"minimal", `{"protocolVersion":1,"agentCapabilities":{}}`, true, false},
		{"load", `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, true, true},
		{"load disabled", `{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}`, true, false},
		{"missing version", `{"agentCapabilities":{}}`, false, false},
		{"missing capabilities", `{"protocolVersion":1}`, false, false},
		{"null", `null`, false, false},
		{"array", `[]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			load, err := initializeCapabilities(json.RawMessage(tc.raw))
			if (err == nil) != tc.valid || load != tc.load {
				t.Fatalf("load=%v err=%v", load, err)
			}
		})
	}
	for _, version := range []string{`null`, `true`, `"1"`, `1.0`, `1.1`, `0`, `2`, `[]`, `{}`} {
		t.Run("version "+version, func(t *testing.T) {
			if _, err := initializeCapabilities(json.RawMessage(`{"protocolVersion":` + version + `,"agentCapabilities":{}}`)); err == nil {
				t.Fatal("accepted invalid protocol version")
			}
		})
	}
	for _, caps := range []string{`null`, `[]`, `true`, `1`, `"secret"`, `{"loadSession":null}`, `{"loadSession":"true"}`} {
		t.Run("capabilities "+caps, func(t *testing.T) {
			if _, err := initializeCapabilities(json.RawMessage(`{"protocolVersion":1,"agentCapabilities":` + caps + `}`)); err == nil {
				t.Fatal("accepted invalid capability object")
			}
		})
	}
	for _, group := range []struct {
		name              string
		booleans, markers []string
	}{
		{"promptCapabilities", []string{"image", "audio", "embeddedContext"}, nil},
		{"mcpCapabilities", []string{"http", "sse"}, nil},
		{"sessionCapabilities", nil, []string{"list", "resume", "close", "delete", "additionalDirectories"}},
		{"auth", nil, []string{"logout"}},
	} {
		for _, value := range []string{`{}`, `null`, `[]`, `false`, `"x"`, `{"_meta":[]}`, `{"_meta":null}`, `{"_meta":{"extension":[null,true]}}`} {
			t.Run(group.name+" "+value, func(t *testing.T) {
				_, err := initializeCapabilities(json.RawMessage(fmt.Sprintf(`{"protocolVersion":1,"agentCapabilities":{%q:%s}}`, group.name, value)))
				valid := strings.HasPrefix(value, `{`) && value != `{"_meta":[]}`
				if (err == nil) != valid {
					t.Fatalf("valid=%v err=%v", valid, err)
				}
			})
		}
		fields := append(append([]string{}, group.booleans...), group.markers...)
		for i, field := range fields {
			for _, value := range []string{`true`, `false`, `null`, `{}`, `[]`, `1`, `"true"`} {
				t.Run(group.name+"/"+field+"/"+value, func(t *testing.T) {
					_, err := initializeCapabilities(json.RawMessage(fmt.Sprintf(`{"protocolVersion":1,"agentCapabilities":{%q:{%q:%s}}}`, group.name, field, value)))
					valid := value == `true` || value == `false`
					if i >= len(group.booleans) {
						valid = value == `null` || value == `{}`
					}
					if (err == nil) != valid {
						t.Fatalf("valid=%v err=%v", valid, err)
					}
				})
			}
		}
	}
	for _, value := range []string{`null`, `true`, `"opaque"`, `17`, `[]`, `{"_meta":false}`} {
		for _, fields := range []string{`"fork":` + value, `"systemPrompt":` + value, `"fork":` + value + `,"systemPrompt":` + value} {
			t.Run("opaque "+fields, func(t *testing.T) {
				raw := `{"protocolVersion":1,"agentCapabilities":{"unknown":` + value + `,"_meta":{"arbitrary":` + value + `},"sessionCapabilities":{` + fields + `}}}`
				if _, err := initializeCapabilities(json.RawMessage(raw)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	for _, meta := range []string{`null`, `{}`, `{"extension":[null,true]}`, `[]`, `true`, `1`, `"x"`} {
		t.Run("root metadata "+meta, func(t *testing.T) {
			_, err := initializeCapabilities(json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"_meta":` + meta + `}}`))
			if (err == nil) != (meta == `null` || strings.HasPrefix(meta, `{`)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestInitializeExactRequiredKeys(t *testing.T) {
	for _, field := range []struct{ name, valid, bad, other string }{
		{"protocolVersion", `1`, `null`, `"agentCapabilities":{}`},
		{"agentCapabilities", `{}`, `null`, `"protocolVersion":1`},
		{"protocolVersion", `1`, `"1"`, `"agentCapabilities":{}`},
		{"agentCapabilities", `{}`, `[]`, `"protocolVersion":1`},
	} {
		for _, alias := range []string{strings.ToUpper(field.name), strings.ToUpper(field.name[:1]) + field.name[1:]} {
			for _, tc := range []struct {
				name, exact, variant string
				valid                bool
			}{
				{"alias only", "", field.valid, false},
				{"invalid exact", field.bad, field.valid, false},
				{"valid exact", field.valid, field.bad, true},
			} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s/reverse=%v", field.name, alias, tc.name, field.bad, reverse), func(t *testing.T) {
						members := fmt.Sprintf(`%q:%s`, alias, tc.variant)
						if tc.exact != "" {
							exact := fmt.Sprintf(`%q:%s`, field.name, tc.exact)
							if reverse {
								members = members + "," + exact
							} else {
								members = exact + "," + members
							}
						}
						_, err := initializeCapabilities(json.RawMessage("{" + field.other + "," + members + "}"))
						if (err == nil) != tc.valid {
							t.Fatalf("valid=%v err=%v", tc.valid, err)
						}
					})
				}
			}
		}
	}
}
