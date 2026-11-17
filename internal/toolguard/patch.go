package toolguard

import (
	"context"
	"io/fs"
	"strings"
)

// JavaScript String.prototype.trim removes WhiteSpace and LineTerminator code
// points. Unlike Go's strings.TrimSpace, it includes U+FEFF and excludes U+0085.
// Droid trims all four patch header targets; keep the raw interpretation too.
const patchTrimSpace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// patchPaths checks the frame and extracts headers, not an applying diff. Body
// text stays opaque, even when it carries a protected spelling or header text
// after a hunk prefix. Cut avoids a scanner's line cap on large carried content.
func patchPaths(ctx context.Context, input string) ([]string, error) {
	line, rest, more := strings.Cut(input, "\n")
	crlf := line == "*** Begin Patch\r"
	if (line != "*** Begin Patch" && !crlf) || !more {
		return nil, fs.ErrInvalid
	}
	var paths []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, rest, more = strings.Cut(rest, "\n")
		if crlf {
			line = strings.TrimSuffix(line, "\r")
		}
		if line == "*** End Patch" {
			if rest != "" {
				return nil, fs.ErrInvalid
			}
			return paths, nil
		}
		if strings.HasPrefix(line, "*** ") {
			path, found := patchHeader(line)
			if !found {
				return nil, fs.ErrInvalid
			}
			trimmed := strings.Trim(path, patchTrimSpace)
			if trimmed == "" {
				return nil, fs.ErrInvalid
			}
			paths = append(paths, path)
			if trimmed != path {
				paths = append(paths, trimmed)
			}
		} else if len(paths) == 0 {
			return nil, fs.ErrInvalid
		}
		if !more {
			return nil, fs.ErrInvalid
		}
	}
}

func patchHeader(line string) (string, bool) {
	for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
		if path, found := strings.CutPrefix(line, prefix); found {
			return path, true // checked both raw and as trimmed by the provider
		}
	}
	return "", false
}
