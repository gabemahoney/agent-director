package model_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// TestWriteEvent pins each event's log line, which macrun verdict reads back
// from a part's job log.
func TestWriteEvent(t *testing.T) {
	t.Parallel()
	if strings.HasPrefix(model.LogMarker, "::") || strings.HasPrefix(model.LogMarker, "##[") {
		t.Errorf("LogMarker %q is a GitHub workflow command or log directive", model.LogMarker)
	}
	identified := model.Event{Part: "api", RunID: 123456789, Attempt: 2, Commit: "0123abcd"}
	partStart, setupFailed := identified, identified
	partStart.Event = model.EventPartStart
	partStart.Versions = []model.Version{{Name: model.VersionGo, Value: "go1.22.10"}}
	partStart.Configurations = []model.Configuration{model.ConfigurationDefault}
	setupFailed.Event = model.EventSetupFailed
	setupFailed.Step, setupFailed.Message = "tools", "bash resolves to\n/opt/homebrew/bin/bash"
	const identity = `"part":"api","run_id":123456789,"attempt":2,"commit":"0123abcd"`
	for _, tc := range []struct {
		event model.Event
		want  string
	}{
		{partStart, `{"event":"part-start",` + identity + `,"versions":[{"name":"go","value":"go1.22.10"}],"configurations":["default"]}`},
		{setupFailed, `{"event":"setup-failed",` + identity + `,"step":"tools","message":"bash resolves to\n/opt/homebrew/bin/bash"}`},
		{model.Event{Event: model.EventPackageBuild, Dir: "pkg/api", Built: true}, `{"event":"package-build","dir":"pkg/api","built":true}`},
		{model.Event{Event: model.EventPackageBuild, Dir: "pkg/broken"}, `{"event":"package-build","dir":"pkg/broken"}`},
		{model.Event{Event: model.EventCaseStart, Case: "go:pkg/api:TestA"}, `{"event":"case-start","case":"go:pkg/api:TestA"}`},
		{
			model.Event{Event: model.EventCaseEnd, Case: "go:pkg/api:TestA", Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 0.5},
			`{"event":"case-end","case":"go:pkg/api:TestA","result":"fail","reason":"failed","elapsed":0.5}`,
		},
		{model.Event{Event: model.EventPartEnd, End: model.EndCancelled}, `{"event":"part-end","end":"cancelled"}`},
	} {
		var b bytes.Buffer
		if err := model.WriteEvent(&b, tc.event); err != nil {
			t.Fatalf("WriteEvent(%s): %v", tc.event.Event, err)
		}
		if want := "@macrun-event " + tc.want + "\n"; b.String() != want {
			t.Errorf("WriteEvent(%s) =\n%q\nwant\n%q", tc.event.Event, b.String(), want)
		}
	}
}

// githubLog joins lines as GitHub's job log download does: a byte-order
// mark, a timestamp and a space before each line, and \r\n line ends.
func githubLog(lines ...string) string {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "2026-11-05T14:00:%02d.1234567Z %s", i%60, line)
	}
	return b.String()
}

// marked returns e's log line without its line end.
func marked(t *testing.T, e model.Event) string {
	t.Helper()
	var b bytes.Buffer
	if err := model.WriteEvent(&b, e); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// overlong is an unmarked line longer than ReadLog's buffer.
var overlong = strings.Repeat("x", 1<<20+1)

func TestReadLog(t *testing.T) {
	t.Parallel()
	versions := []model.Version{{Name: model.VersionGo, Value: "go1.22.10"}, {Name: model.VersionBun, Value: "1.1.38"}}
	start := func(id model.CaseID) string { return marked(t, model.Event{Event: model.EventCaseStart, Case: id}) }
	end := func(id model.CaseID, r model.Result, elapsed float64) string {
		e := model.Event{Event: model.EventCaseEnd, Case: id, Result: r, Elapsed: elapsed}
		if r == model.ResultFail {
			e.Reason = model.ReasonFailed
		}
		return marked(t, e)
	}
	for _, tc := range []struct {
		name  string
		lines []string
		want  model.Record
	}{
		{
			name: "finished part",
			lines: []string{
				marked(t, model.Event{
					Event: model.EventPartStart, Part: "api", RunID: 123456789, Attempt: 2, Commit: "0123abcd",
					Versions: versions, Configurations: []model.Configuration{model.ConfigurationDefault},
				}),
				"::stop-commands::f00d",
				marked(t, model.Event{Event: model.EventPackageBuild, Dir: "pkg/a", Built: true}),
				start("go:pkg/a"),
				start("go:pkg/a:TestA"),
				"    === RUN   TestA",
				"    fake output: " + end("go:pkg/a:TestX", model.ResultFail, 0),
				"@macrun-event-ish {not json",
				overlong,
				end("go:pkg/a:TestA", model.ResultPass, 0.25),
				// TestA again, from the package's external test file.
				start("go:pkg/a:TestA"),
				end("go:pkg/a:TestA", model.ResultFail, 0.5),
				end("go:pkg/a", model.ResultPass, 1.5),
				marked(t, model.Event{Event: model.EventPackageBuild, Dir: "pkg/b", Built: true}),
				start("go:pkg/b"),
				start("go:pkg/b:TestB"),
				start("go:pkg/b:TestS"),
				end("go:pkg/b:TestS", model.ResultSkip, 0),
				marked(t, model.Event{Event: model.EventPackageBuild, Dir: "pkg/c"}),
				"::f00d::",
				marked(t, model.Event{Event: model.EventPartEnd, End: model.EndFinished}),
			},
			want: model.Record{
				SchemaVersion:  model.RecordSchemaVersion,
				Part:           "api",
				RunID:          123456789,
				Attempt:        2,
				Commit:         "0123abcd",
				Versions:       versions,
				Configurations: []model.Configuration{model.ConfigurationDefault},
				Packages:       []model.PackageBuild{{Dir: "pkg/a", Built: true}, {Dir: "pkg/b", Built: true}, {Dir: "pkg/c"}},
				Cases: []model.CaseRecord{
					{CaseResult: model.CaseResult{Case: "go:pkg/a:TestA", Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 0.75}},
					{CaseResult: model.CaseResult{Case: "go:pkg/a", Result: model.ResultPass, Elapsed: 1.5}},
					{CaseResult: model.CaseResult{Case: "go:pkg/b:TestS", Result: model.ResultSkip}},
				},
				WithoutResult: []model.CaseID{"go:pkg/b", "go:pkg/b:TestB"},
				End:           model.EndFinished,
				FromLog:       true,
			},
		},
		{
			name: "setup failure",
			lines: []string{
				"##[group]Run tools/macrun part",
				marked(t, model.Event{
					Event: model.EventSetupFailed, Part: "api", RunID: 123456789, Attempt: 2, Commit: "0123abcd",
					Step: "tools", Message: "bash resolves to /opt/homebrew/bin/bash",
				}),
			},
			want: model.Record{
				SchemaVersion: model.RecordSchemaVersion,
				Part:          "api",
				RunID:         123456789,
				Attempt:       2,
				Commit:        "0123abcd",
				SetupFailure:  &model.SetupFailure{Step: "tools", Message: "bash resolves to /opt/homebrew/bin/bash"},
				FromLog:       true,
			},
		},
	} {
		got, ok, err := model.ReadLog(strings.NewReader(githubLog(tc.lines...)))
		if err != nil || !ok {
			t.Fatalf("%s: ReadLog = ok %v, %v; want a record", tc.name, ok, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ReadLog =\n%+v\nwant\n%+v", tc.name, got, tc.want)
		}
	}
}

func TestReadLogFaults(t *testing.T) {
	t.Parallel()
	partStart := marked(t, model.Event{Event: model.EventPartStart, Part: "api"})
	for _, tc := range []struct {
		name  string
		lines []string
		// wantErr is the error's start; empty when ReadLog finds no marked line.
		wantErr string
	}{
		{
			name:    "malformed marked line",
			lines:   []string{partStart, overlong, "@macrun-event {\"event\":"},
			wantErr: "job log line 3: marked line doesn't parse: ",
		},
		{
			name:    "unknown event",
			lines:   []string{partStart, `@macrun-event {"event":"case-skip"}`},
			wantErr: `job log line 2: marked line has unknown event "case-skip"`,
		},
		{name: "no marked line", lines: []string{"##[group]Set up job", "    @macrun-event {"}},
	} {
		_, ok, err := model.ReadLog(strings.NewReader(githubLog(tc.lines...)))
		switch {
		case tc.wantErr == "" && (ok || err != nil):
			t.Errorf("%s: ReadLog = ok %v, %v; want no record and no error", tc.name, ok, err)
		case tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)):
			t.Errorf("%s: ReadLog error = %v; want it to start %q", tc.name, err, tc.wantErr)
		}
	}
}
