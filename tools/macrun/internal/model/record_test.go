package model_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// TestRecordJSON pins the part record's JSON form, which macrun verdict and
// later Epics read from mac-part-<part>-<attempt>.
func TestRecordJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		record model.Record
		want   string
	}{
		{
			name: "finished",
			record: model.Record{
				SchemaVersion: model.RecordSchemaVersion,
				Part:          "api",
				RunID:         123456789,
				Attempt:       2,
				Commit:        "0123abcd",
				Versions: []model.Version{
					{Name: model.VersionMacOSProduct, Value: "15.1"},
					{Name: model.VersionMacOSBuild, Value: "24B83"},
					{Name: model.VersionTmux, Value: "tmux 3.5a"},
					{Name: model.VersionMacOSSQLite3, Value: "3.43.2"},
					{Name: model.VersionGo, Value: "go1.22.10"},
					{Name: model.VersionBun, Value: "1.1.38"},
					{Name: model.VersionBash, Value: "GNU bash, version 5.2.37(1)-release"},
					{Name: model.VersionStandardSQLite3, Value: "3.47.0"},
				},
				Configurations: []model.Configuration{model.ConfigurationDefault},
				ExpectedCases:  []model.CaseID{"go:pkg/api", "go:pkg/api:TestA", "go:pkg/api:TestB", "go:pkg/broken:TestC"},
				Packages: []model.PackageBuild{
					{Dir: "pkg/api", Built: true},
					{Dir: "pkg/broken", Output: "syntax error\n", OutputCut: true},
				},
				Cases: []model.CaseRecord{
					{CaseResult: model.CaseResult{Case: "go:pkg/api:TestB", Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 1.5}, Output: "boom\n", OutputCut: true},
					{CaseResult: model.CaseResult{Case: "go:pkg/api:TestA", Result: model.ResultSkip}, Output: "skipped\n"},
					{CaseResult: model.CaseResult{Case: "go:pkg/api", Result: model.ResultPass, Elapsed: 2}},
				},
				WithoutResult: []model.CaseID{"go:pkg/broken:TestC"},
				End:           model.EndFinished,
			},
			want: `{"schema_version":1,"part":"api","run_id":123456789,"attempt":2,"commit":"0123abcd",` +
				`"versions":[{"name":"macos-product-version","value":"15.1"},{"name":"macos-build-version","value":"24B83"},` +
				`{"name":"tmux","value":"tmux 3.5a"},{"name":"macos-sqlite3","value":"3.43.2"},{"name":"go","value":"go1.22.10"},` +
				`{"name":"bun","value":"1.1.38"},{"name":"bash","value":"GNU bash, version 5.2.37(1)-release"},` +
				`{"name":"standard-sqlite3","value":"3.47.0"}],` +
				`"configurations":["default"],` +
				`"expected_cases":["go:pkg/api","go:pkg/api:TestA","go:pkg/api:TestB","go:pkg/broken:TestC"],` +
				`"packages":[{"dir":"pkg/api","built":true},{"dir":"pkg/broken","built":false,"output":"syntax error\n","output_cut":true}],` +
				`"cases":[{"case":"go:pkg/api:TestB","result":"fail","reason":"failed","elapsed":1.5,"output":"boom\n","output_cut":true},` +
				`{"case":"go:pkg/api:TestA","result":"skip","elapsed":0,"output":"skipped\n"},` +
				`{"case":"go:pkg/api","result":"pass","elapsed":2}],` +
				`"without_result":["go:pkg/broken:TestC"],"end":"finished"}`,
		},
		{
			name: "setup failure, read from a log",
			record: model.Record{
				SchemaVersion: model.RecordSchemaVersion,
				Part:          "api",
				RunID:         123456789,
				Attempt:       2,
				Commit:        "0123abcd",
				SetupFailure:  &model.SetupFailure{Step: "github-hosted runner", Message: "runner environment is self-hosted"},
				FromLog:       true,
			},
			want: `{"schema_version":1,"part":"api","run_id":123456789,"attempt":2,"commit":"0123abcd",` +
				`"setup_failure":{"step":"github-hosted runner","message":"runner environment is self-hosted"},"from_log":true}`,
		},
	} {
		for range 2 {
			got, err := json.Marshal(tc.record)
			if err != nil {
				t.Fatalf("%s: Marshal: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s: Marshal =\n%s\nwant\n%s", tc.name, got, tc.want)
			}
		}
		var back model.Record
		if err := json.Unmarshal([]byte(tc.want), &back); err != nil {
			t.Fatalf("%s: Unmarshal: %v", tc.name, err)
		}
		if !reflect.DeepEqual(back, tc.record) {
			t.Errorf("%s: round trip = %+v; want %+v", tc.name, back, tc.record)
		}
	}
}
