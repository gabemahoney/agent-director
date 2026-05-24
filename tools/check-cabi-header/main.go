// Command check-cabi-header validates that every C function declaration in a
// CGO-generated header starts with the required "ad_" prefix.
//
// Usage:
//
//	go run ./tools/check-cabi-header [path/to/header.h]
//
// The path defaults to dist/libagent_director.h when omitted.
// Exits 0 when all declarations pass; exits 1 and prints each offending
// symbol to stderr when any declaration violates the naming rule.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const defaultHeader = "dist/libagent_director.h"
const requiredPrefix = "ad_"

func main() {
	path := defaultHeader
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	violations, err := checkHeader(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-cabi-header: %v\n", err)
		os.Exit(1)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "check-cabi-header: %d function(s) in %s lack the %q prefix:\n", len(violations), path, requiredPrefix)
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "  %s\n", v)
		}
		os.Exit(1)
	}
}

// checkHeader reads the header at path and returns the names of any exported
// C functions that do not start with requiredPrefix. An error is returned only
// for I/O failures; a non-conforming header yields a non-empty violations
// slice, not an error.
func checkHeader(path string) (violations []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		name, ok := extractFuncName(line)
		if !ok {
			continue
		}
		if !strings.HasPrefix(name, requiredPrefix) {
			violations = append(violations, name)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return violations, nil
}

// extractFuncName parses a single line from a CGO-generated C header and
// returns the function name if the line is an extern function declaration
// that names a user-exported symbol.
//
// CGO emits two kinds of extern declarations:
//
//  1. User exports (from //export directives):
//     extern char* ad_open(char* params_json);
//
//  2. CGO runtime helpers (_GoStringLen, _GoStringPtr, …):
//     extern size_t _GoStringLen(_GoString_ s);
//
// Category 2 symbols start with '_', which is the C standard's
// implementation-reserved namespace. They are skipped so the validator
// checks only the user-controlled surface.
func extractFuncName(line string) (name string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "extern ") {
		return "", false
	}
	parenIdx := strings.Index(trimmed, "(")
	if parenIdx < 0 {
		return "", false
	}
	// Everything between "extern " and "(" holds the return type + func name.
	before := strings.TrimSpace(trimmed[len("extern "):parenIdx])
	// Split on whitespace and pointer stars to isolate the last token.
	parts := strings.FieldsFunc(before, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '*'
	})
	if len(parts) == 0 {
		return "", false
	}
	candidate := parts[len(parts)-1]
	// Skip CGO runtime helpers — they are implementation-reserved (_-prefixed)
	// and are not part of the user-visible C ABI.
	if strings.HasPrefix(candidate, "_") {
		return "", false
	}
	return candidate, true
}
