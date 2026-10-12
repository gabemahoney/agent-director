package codexfix

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Override is one decoded `-c key=value` value, read the way Codex reads it
// (codex-rs/utils/cli config_override.rs): the text is split at the first "="
// into a key and a value, each trimmed; the key path is the key split on "."
// (no quoting, so a dotted segment cannot be written); the value is parsed as
// TOML by wrapping it as `_x_ = <value>`, and a value that does not parse
// becomes a string with its surrounding quotes trimmed.
type Override struct {
	// Raw is the whole `key=value` text as passed.
	Raw string
	// Key is the trimmed text before the first "=".
	Key string
	// Path is Key split on ".".
	Path []string
	// RawValue is the trimmed text after the first "=".
	RawValue string
	// Value is the decoded TOML value: bool, int64, float64, string,
	// []any or map[string]any (a TOML array of tables is []map[string]any).
	// For a value that did not parse it is the trimmed string.
	Value any
	// ParseErr is the TOML error when RawValue did not parse as TOML and
	// Codex would fall back to a string; nil when it parsed.
	ParseErr error
}

// ParseOverride decodes the `key=value` text of one -c override. It returns an
// error only for the cases Codex rejects (no "=", an empty key); a value that
// is not TOML decodes as a string with ParseErr set, as Codex falls back.
func ParseOverride(s string) (Override, error) {
	key, val, ok := strings.Cut(s, "=")
	if !ok {
		return Override{}, fmt.Errorf("invalid override (missing '='): %q", s)
	}
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if key == "" {
		return Override{}, fmt.Errorf("empty key in override: %q", s)
	}
	o := Override{Raw: s, Key: key, Path: strings.Split(key, "."), RawValue: val}
	var doc map[string]any
	if _, err := toml.Decode("_x_ = "+val, &doc); err != nil {
		o.ParseErr = err
		o.Value = strings.Trim(val, `"'`)
		return o, nil
	}
	o.Value = doc["_x_"]
	return o, nil
}

// PathIs reports whether the override's key path equals the dotted path.
func (o Override) PathIs(dotted string) bool {
	return strings.Join(o.Path, ".") == dotted
}

// PathHasPrefix reports whether the override's key path starts with the
// dotted prefix, segment by segment (the override is at or under the prefix).
func (o Override) PathHasPrefix(dotted string) bool {
	want := strings.Split(dotted, ".")
	if len(o.Path) < len(want) {
		return false
	}
	for i, seg := range want {
		if o.Path[i] != seg {
			return false
		}
	}
	return true
}

// ShellWords reads s the way a POSIX shell reads one command line into words:
// unquoted text split on blanks, single quotes taking everything literally,
// double quotes honouring \\ \" \$ \` escapes, and a backslash outside quotes
// escaping the next character. It is how a test reads back a hook command AD
// quoted (SR-5.3: a single quote is written close-quote, backslash, single
// quote, open-quote; a backslash is written close-quote, two backslashes,
// open-quote). It does no expansion, no globbing and no operators; an
// unterminated quote is an error.
func ShellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case ' ', '\t', '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case '\'':
			inWord = true
			i++
			for i < len(rs) && rs[i] != '\'' {
				cur.WriteRune(rs[i])
				i++
			}
			if i >= len(rs) {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
		case '"':
			inWord = true
			i++
			for i < len(rs) && rs[i] != '"' {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\\\"$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
				i++
			}
			if i >= len(rs) {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
		case '\\':
			inWord = true
			if i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
			}
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
