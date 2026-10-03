package tla_test

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// loadSuite8 reads a manifest with the props column: its rows and each row's props.
func loadSuite8(t *testing.T, path string) ([]row, []string) {
	t.Helper()
	var rows []row
	var props []string
	for _, line := range strings.Split(readFile(t, path), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			t.Fatalf("%s: bad row %q", path, line)
		}
		c, err := strconv.Atoi(f[3])
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row{f[0], f[1], f[2], c, f[4], f[5], f[6]})
		props = append(props, f[7])
	}
	return rows, props
}

// fields is rw as a manifest line, with any extra columns appended.
func fields(rw row, extra ...string) string {
	return strings.Join(append([]string{rw.group, rw.spec, rw.cfg, strconv.Itoa(rw.capS), rw.expect, rw.tier, rw.what}, extra...), "\t")
}

// writeManifest writes lines, after a comment line, as dir/suite.tsv and
// returns its path; the first line is line 2.
func writeManifest(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, "suite.tsv")
	writeFile(t, p, "# test manifest\n"+strings.Join(lines, "\n")+"\n", 0o644)
	return p
}

// TestRunnerLaunchSuite runs spec/tla/launch/suite.tsv: its props column is
// accepted, its cfgs come from launch/cfg/ (unpinned) into the job contexts,
// and every row gets the violation its props name.
func TestRunnerLaunchSuite(t *testing.T) {
	r := newRig(t)
	suite := filepath.Join(r.tree, "launch", "suite.tsv")
	cfgDir, err := filepath.EvalSymlinks(filepath.Join(r.tree, "launch", "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	rows, props := loadSuite8(t, suite)
	for i, rw := range rows {
		res := "PASS"
		if rw.expect == "violation" {
			names := strings.Split(props[i], ",")
			res = "FAIL(Invariant_" + names[len(names)-1] + "_is_violated)"
		}
		r.verdict(t, rw.cfg, res)
	}
	r.env["TLA_SUITE"] = suite
	r.env["TLA_TIER"] = "full"

	res := r.run(t)

	want := fmt.Sprintf(`^CI-VERDICT PASS \(%d/%d runs ok, \d+ min, tier full\)$`, len(rows), len(rows))
	if res.code != 0 || !regexp.MustCompile(want).MatchString(res.last()) {
		t.Fatalf("want exit 0 and %s\n%s", want, res)
	}
	lines := res.lines()
	for i, rw := range rows {
		m := runLine.FindStringSubmatch(lines[i])
		if m == nil || m[1] != "PASS" || m[2] != rw.cfg || m[6] != rw.what {
			t.Errorf("run line %d = %q, want a PASS line for %s -- %s", i, lines[i], rw.cfg, rw.what)
		}
	}
	groups := groupsOf(rows)
	if got := r.submits(t); !reflect.DeepEqual(got, append([]string{"parse"}, groups...)) {
		t.Errorf("submits = %v, want parse then %v", got, groups)
	}
	checkJob(t, r, "parse", parseRuns(rows), 600)
	for _, g := range groups {
		runs, budget := groupRuns(rows, g)
		checkJob(t, r, g, runs, budget)
		for _, line := range runs {
			c := strings.Fields(line)[1]
			sum := fmt.Sprintf("%x  ./%s.cfg", sha256.Sum256([]byte(readFile(t, filepath.Join(cfgDir, c+".cfg")))), c)
			if !strings.Contains(r.seen(t, g, "cfgs"), sum) {
				t.Errorf("job %s context lacks launch/cfg/%s.cfg as it is on disk:\n%s", g, c, r.seen(t, g, "cfgs"))
			}
		}
	}
	if note := fmt.Sprintf("# %d cfgs from %s (next to TLA_SUITE; not pinned)", len(rows), cfgDir); !strings.Contains(res.stderr, note) {
		t.Errorf("stderr lacks %q\n%s", note, res)
	}
	if exists(filepath.Join(r.fake, "forbidden")) {
		t.Errorf("the runner started a forbidden tool: %s", readFile(t, filepath.Join(r.fake, "forbidden")))
	}

	t.Run("print", func(t *testing.T) {
		p := newRig(t)
		p.env["TLA_SUITE"] = suite

		res := p.run(t, "--print")

		if res.code != 0 {
			t.Fatalf("want exit 0\n%s", res)
		}
		if line := fmt.Sprintf("  cfgs:       %d from %s (next to TLA_SUITE; not pinned)", len(rows), cfgDir); !strings.Contains(res.stdout, line) {
			t.Errorf("stdout lacks %q\n%s", line, res)
		}
		if p.calls(t) != nil {
			t.Errorf("--print called the scheduler: %v", p.calls(t))
		}
	})
}

// TestRunnerPropsRules: with the props column, a violation row is ok only
// when its result names one of its props; a pass
// row is judged as without it.
func TestRunnerPropsRules(t *testing.T) {
	cases := []struct {
		name, expect, props, result string
		ok, note                    bool // note: the line ends "(the expected violation is <props>)"
	}{
		{"invariant named", "violation", "Safe", "FAIL(Invariant_Safe_is_violated)", true, false},
		{"action property, second of two", "violation", "Safe,Live", "FAIL(Action_property_Live_is_violated)", true, false},
		{"temporal property named", "violation", "Live", "FAIL(Temporal_property_Live_was_violated)", true, false},
		{"another property", "violation", "Safe", "FAIL(Invariant_Other_is_violated)", false, true},
		{"prop is a longer name", "violation", "NotSafe", "FAIL(Invariant_Safe_is_violated)", false, true},
		{"prop inside the result's name", "violation", "Safe", "FAIL(Invariant_NotSafe_is_violated)", false, true},
		{"temporal properties, none named", "violation", "Live", "FAIL(Temporal_properties_were_violated)", false, true},
		{"violation row, PASS", "violation", "Safe", "PASS", false, false},
		{"violation row, INCOMPLETE", "violation", "Safe", "INCOMPLETE(budget)", false, false},
		{"pass row, PASS", "pass", "-", "PASS", true, false},
		{"pass row, FAIL", "pass", "-", "FAIL(Invariant_Safe_is_violated)", false, false},
	}
	r := newRig(t)
	pool := inTier(shipped(t), "fast")
	var lines []string
	ok := 0
	for i, c := range cases {
		rw := pool[i]
		rw.group, rw.expect, rw.what = "g1", c.expect, c.name
		lines = append(lines, fields(rw, c.props))
		r.verdict(t, rw.cfg, c.result)
		if c.ok {
			ok++
		}
	}
	r.env["TLA_SUITE"] = writeManifest(t, t.TempDir(), lines...)

	res := r.run(t)

	want := fmt.Sprintf(`^CI-VERDICT FAIL \(%d/%d runs ok, \d+ min, tier fast\)$`, ok, len(cases))
	if res.code != 1 || !regexp.MustCompile(want).MatchString(res.last()) {
		t.Fatalf("want exit 1 and %s\n%s", want, res)
	}
	out := res.lines()
	for i, c := range cases {
		m := runLine.FindStringSubmatch(out[i])
		tag, what := "FAIL", c.name
		if c.ok {
			tag = "PASS"
		}
		if c.note {
			what += " (the expected violation is " + c.props + ")"
		}
		if m == nil || m[1] != tag || m[2] != pool[i].cfg || m[3] != c.result || m[6] != what {
			t.Errorf("%s: line %q, want %s %s %s -- %s", c.name, out[i], tag, pool[i].cfg, c.result, what)
		}
	}
	if strings.Contains(res.stderr, "cfgs from") {
		t.Errorf("ci/cfg cfgs reported as unpinned\n%s", res)
	}
}

// TestRunnerPropsRefusals: a bad props column, or a cfg next to the manifest
// that is ambiguous or missing, fails preflight before the scheduler is called.
func TestRunnerPropsRefusals(t *testing.T) {
	all := shipped(t)
	v, p := byCfg(t, all, "ci_C1"), byCfg(t, all, "ci_S1h_lab")
	v.expect, p.expect = "violation", "pass"
	v.tier, p.tier = "fast", "fast"
	ls := v
	ls.cfg = "ls_test"
	cases := []struct {
		name   string
		lines  []string
		cfgs   []string // files made in cfg/ next to the manifest
		reason string
		fail   string
	}{
		{name: "8 then 7 fields", lines: []string{fields(v, "Safe"), fields(p)}, reason: "bad manifest",
			fail: "line 3: has 7 fields, not 8; nothing was submitted"},
		{name: "7 then 8 fields", lines: []string{fields(p), fields(v, "Safe")}, reason: "bad manifest",
			fail: "line 3: has 8 fields, not 7; nothing was submitted"},
		{name: "props on a pass row", lines: []string{fields(p, "Safe")}, reason: "bad manifest",
			fail: `line 2: props "Safe" on a pass row, not "-"`},
		{name: "dash on a violation row", lines: []string{fields(v, "-")}, reason: "bad manifest", fail: `line 2: bad props "-"`},
		{name: "empty props", lines: []string{fields(v, "")}, reason: "bad manifest", fail: `line 2: bad props ""`},
		{name: "empty prop in the list", lines: []string{fields(v, "Safe,,Live")}, reason: "bad manifest", fail: `line 2: bad props "Safe,,Live"`},
		{name: "space in the list", lines: []string{fields(v, "Safe, Live")}, reason: "bad manifest", fail: `line 2: bad props "Safe, Live"`},
		{name: "cfg in both dirs", lines: []string{fields(v, "Safe")}, cfgs: []string{"ci_C1"}, reason: "cfg ambiguous",
			fail: "FAIL preflight -- ci_C1.cfg is in both ci/cfg/ and "},
		{name: "cfg in neither dir", lines: []string{fields(ls, "Safe")}, cfgs: []string{"ls_other"}, reason: "cfg missing",
			fail: "FAIL preflight -- ls_test.cfg (manifest row for ls_test) is in neither ci/cfg/ nor "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			dir := t.TempDir()
			for _, c := range tc.cfgs {
				writeFile(t, filepath.Join(dir, "cfg", c+".cfg"), "SPECIFICATION Spec\n", 0o644)
			}
			r.env["TLA_SUITE"] = writeManifest(t, dir, tc.lines...)

			res := r.run(t)

			if want := "CI-VERDICT FAIL (" + tc.reason + ", tier fast)"; res.code != 1 || res.last() != want {
				t.Fatalf("want exit 1 and %q\n%s", want, res)
			}
			if !strings.Contains(res.stdout, tc.fail) {
				t.Errorf("stdout lacks %q\n%s", tc.fail, res)
			}
			if got := r.calls(t); got != nil {
				t.Errorf("scheduler calls = %v, want none", got)
			}
		})
	}
}
