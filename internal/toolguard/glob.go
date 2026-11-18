package toolguard

import (
	"context"
	"io/fs"
	"strings"
)

func (r pathResolver) checkGlob(root pathForms, patterns, secrets []string) reason {
	// Keep the raw root for lexical-first resolution of the entire pattern,
	// and each resolved root for relative patterns in all root interpretations.
	bases := append(append([]string(nil), root.absolute...), root.resolved...)
	for _, pattern := range patterns {
		literal, err := globLiteralFolder(r.ctx, pattern)
		if err != nil {
			return checkFailed
		}
		if decision := r.checkTarget(literal, bases, secrets, true); decision != noDecision {
			return decision
		}
	}
	return noDecision
}

// globLiteralFolder bounds a pattern without expanding it or listing matches.
// Inspect uncleaned components: cleaning first would erase a wildcard/.. escape.
func globLiteralFolder(ctx context.Context, pattern string) (string, error) {
	if strings.IndexByte(pattern, 0) >= 0 {
		return "", fs.ErrInvalid
	}
	if err := checkGlobBraces(ctx, pattern); err != nil {
		return "", err
	}
	components := strings.Split(pattern, "/")
	firstWildcard := len(components)
	for i, component := range components {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if firstWildcard == len(components) && strings.ContainsAny(component, "*?[{\\") {
			firstWildcard = i
		}
		if firstWildcard <= i && globParentComponent(component) {
			return "", fs.ErrInvalid
		}
	}
	literal := strings.Join(components[:firstWildcard], "/")
	if literal == "" && strings.HasPrefix(pattern, "/") {
		literal = "/"
	}
	return literal, nil
}

// Backslash is itself a wildcard-boundary character, but quoting dots must not
// conceal a parent component from the bound check.
func globParentComponent(component string) bool {
	dots := 0
	for i := 0; i < len(component); i++ {
		if component[i] == '\\' {
			i++
		}
		if i == len(component) || component[i] != '.' {
			return false
		}
		dots++
	}
	return dots == 2
}

// checkGlobBraces sees groups across '/' separators. It tracks only syntax
// relevant to bounds; classes and escaped braces are not brace groups. Other
// pattern syntax remains the tool's responsibility.
func checkGlobBraces(ctx context.Context, pattern string) error {
	var starts []bool // whether each group's current alternative is still empty
	inClass := false
	var previous byte
	for i := 0; i < len(pattern); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		c := pattern[i]
		escaped := c == '\\'
		if escaped {
			if i+1 == len(pattern) {
				return fs.ErrInvalid
			}
			i++
			c = pattern[i]
		}
		if len(starts) > 0 {
			if c == '/' || (c == '.' && previous == '.') || (c == '~' && starts[len(starts)-1]) {
				return fs.ErrInvalid
			}
		}
		previous = c
		if escaped {
			if len(starts) > 0 {
				starts[len(starts)-1] = false
			}
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			}
			continue
		}
		switch c {
		case '[':
			inClass = true
		case '{':
			if len(starts) > 0 {
				starts[len(starts)-1] = false
			}
			starts = append(starts, true)
			continue
		case '}':
			if len(starts) > 0 {
				starts = starts[:len(starts)-1]
			}
		case ',':
			if len(starts) > 0 {
				starts[len(starts)-1] = true
			}
			continue
		}
		if len(starts) > 0 {
			starts[len(starts)-1] = false
		}
	}
	if len(starts) != 0 {
		return fs.ErrInvalid
	}
	return nil
}
