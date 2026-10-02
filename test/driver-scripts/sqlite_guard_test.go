package driverscripts_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sqlite3Word is sqlite3 as a whole word, path-prefixed or not
// (/usr/bin/sqlite3), but not inside a longer name (sqlite3_analyzer,
// libsqlite3, state.sqlite3).
var sqlite3Word = regexp.MustCompile(`(?:^|[^\w.])sqlite3(?:\W|$)`)

var (
	fenceOpen  = regexp.MustCompile("^\\s*```+\\s*([A-Za-z]*)")
	fenceClose = regexp.MustCompile("^\\s*```+\\s*$")
)

// numbered is one source line and its 1-based line number.
type numbered struct {
	n    int
	text string
}

// shellCodeOf is the lines inside a markdown file's bash/sh/shell fences.
func shellCodeOf(md string) (code []numbered, blocks int) {
	lang, inFence := "", false
	for i, line := range strings.Split(md, "\n") {
		switch {
		case !inFence && fenceOpen.MatchString(line):
			inFence, lang = true, fenceOpen.FindStringSubmatch(line)[1]
			if lang == "bash" || lang == "sh" || lang == "shell" {
				blocks++
			}
		case inFence && fenceClose.MatchString(line):
			inFence = false
		case inFence && (lang == "bash" || lang == "sh" || lang == "shell"):
			code = append(code, numbered{i + 1, line})
		}
	}
	return code, blocks
}

// bareSqlite3Lines is the line numbers of shell code naming sqlite3. Only
// full-line comments are skipped: strings and trailing comments count.
func bareSqlite3Lines(code []numbered) []int {
	var hits []int
	for _, l := range code {
		if !strings.HasPrefix(strings.TrimSpace(l.text), "#") && sqlite3Word.MatchString(l.text) {
			hits = append(hits, l.n)
		}
	}
	return hits
}

func asLines(src string) []numbered {
	var out []numbered
	for i, line := range strings.Split(src, "\n") {
		out = append(out, numbered{i + 1, line})
	}
	return out
}

// TestNoBareSqlite3InCasesOrDriver: no testplan shell block and no driver
// script but sql.sh names sqlite3 outside a comment; a bare sqlite3 has no
// busy timeout and fails at once with "database is locked" (b.ai5).
func TestNoBareSqlite3InCasesOrDriver(t *testing.T) {
	root := repoRoot(t)
	var offences []string
	report := func(path string, src []numbered) {
		rel, _ := filepath.Rel(root, path)
		text := map[int]string{}
		for _, l := range src {
			text[l.n] = l.text
		}
		for _, n := range bareSqlite3Lines(src) {
			offences = append(offences, fmt.Sprintf("%s:%d: %s", rel, n, strings.TrimSpace(text[n])))
		}
	}
	var blocks, scripts int
	walk := func(dir string, visit func(path, body string)) {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			visit(path, string(b))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	markdown := func(path, body string) {
		if strings.HasSuffix(path, ".md") {
			code, n := shellCodeOf(body)
			blocks += n
			report(path, code)
		}
	}
	walk(filepath.Join(root, "tickets", "testplans"), markdown)
	walk(filepath.Join(root, "test", "driver"), func(path, body string) {
		switch {
		case path == filepath.Join(root, "test", "driver", "sql.sh"): // the one allowed sqlite3 caller
		case strings.HasSuffix(path, ".md"):
			markdown(path, body)
		case strings.HasPrefix(body, "#!"):
			scripts++
			report(path, asLines(body))
		}
	})
	if blocks == 0 || scripts == 0 {
		t.Fatalf("scanned %d testplan shell blocks and %d driver scripts: want both > 0 (did the layout move?)", blocks, scripts)
	}
	if len(offences) > 0 {
		shown := offences
		if len(shown) > 20 {
			shown = append(shown[:20:20], fmt.Sprintf("... and %d more", len(offences)-20))
		}
		t.Errorf("%d shell lines name sqlite3 outside a full-line comment; run /opt/driver/sql.sh (test/driver/sql.sh beside a driver script) instead:\n%s",
			len(offences), strings.Join(shown, "\n"))
	}
}

// TestBareSqlite3Detection pins what the guard flags: sqlite3 anywhere in
// shell code but a full-line comment, and not in prose or other fences.
func TestBareSqlite3Detection(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   string // a markdown body; its shell fences are scanned
		want []int
	}{
		{"plain command", "```bash\nsqlite3 \"$db\" .tables\n```", []int{2}},
		{"inside a quoted string", "```bash\nbash -c \"sqlite3 -readonly $db 'SELECT 1'\"\ntmux send-keys -t \"$p\" \"sqlite3 $db\" Enter\n```", []int{2, 3}},
		{"redirect first", "```sh\n2>/dev/null sqlite3 \"$db\" .tables\n```", []int{2}},
		{"wrapper with options", "```bash\ntimeout -k 1 5 /usr/bin/sqlite3 \"$db\" .tables\n```", []int{2}},
		{"full-line comment", "```bash\n# read it with sqlite3\n  # sqlite3 \"$db\" .tables\n```", nil},
		{"prose and other fences", "Read it with `sqlite3`.\nsqlite3 \"$db\"\n```json\nsqlite3 \"$db\"\n```", nil},
		{"sql.sh and longer names", "```bash\n/opt/driver/sql.sh \"$db\" .tables\nsqlite3_analyzer state.sqlite3 libsqlite3\n```", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := shellCodeOf(tc.md)
			got := bareSqlite3Lines(code)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("flagged lines %v, want %v in:\n%s", got, tc.want, tc.md)
			}
		})
	}
}
