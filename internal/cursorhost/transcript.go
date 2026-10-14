package cursorhost

import "strings"

func splitPath(path string) []string { return strings.Split(strings.TrimPrefix(path, "/"), "/") }
