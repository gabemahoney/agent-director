package model_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// TestPlanJSON pins the plan's JSON form, which mac.yml, part and verdict read.
func TestPlanJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		plan model.Plan
		want string
	}{
		{
			name: "planned",
			plan: model.Plan{
				SchemaVersion: model.PlanSchemaVersion,
				Status:        model.StatusPlanned,
				Coverage:      model.Coverage{Go: model.CoverageFull, Testplan: model.CoveragePartial},
				Parts: []model.Part{{
					Name:      "api",
					Suite:     model.SuiteGo,
					Selection: "pkg/api:^TestA$",
					Packages:  []model.Package{{Dir: "pkg/api", Tests: []string{"TestA"}}},
					ExpectedCases: []model.CaseID{
						model.GoPackageCase("pkg/api"),
						model.GoTestCase("pkg/api", "TestA"),
					},
				}},
			},
			want: `{"schema_version":1,"status":"planned","coverage":{"go":"full","testplan":"partial"},` +
				`"parts":[{"name":"api","suite":"go","selection":"pkg/api:^TestA$",` +
				`"packages":[{"dir":"pkg/api","tests":["TestA"]}],"expected_cases":["go:pkg/api","go:pkg/api:TestA"]}]}`,
		},
		{
			name: "invalid setup",
			plan: model.Plan{
				SchemaVersion: model.PlanSchemaVersion,
				Status:        model.StatusInvalidSetup,
				Reason:        "The setup failed validation.",
				Errors:        []model.SetupError{{File: "test/mac/parts.txt", Line: 3, Message: "bad"}},
				Coverage:      model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial},
			},
			want: `{"schema_version":1,"status":"invalid-setup","reason":"The setup failed validation.",` +
				`"errors":[{"file":"test/mac/parts.txt","line":3,"message":"bad"}],` +
				`"coverage":{"go":"partial","testplan":"partial"}}`,
		},
		{
			name: "no Mac Tests",
			plan: model.Plan{
				SchemaVersion: model.PlanSchemaVersion,
				Status:        model.StatusNoMacTests,
				Reason:        "No test/mac/parts.txt.",
				Coverage:      model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial},
			},
			want: `{"schema_version":1,"status":"no-mac-tests","reason":"No test/mac/parts.txt.",` +
				`"coverage":{"go":"partial","testplan":"partial"}}`,
		},
	} {
		for range 2 {
			got, err := json.Marshal(tc.plan)
			if err != nil {
				t.Fatalf("%s: Marshal: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s: Marshal =\n%s\nwant\n%s", tc.name, got, tc.want)
			}
		}
		var back model.Plan
		if err := json.Unmarshal([]byte(tc.want), &back); err != nil {
			t.Fatalf("%s: Unmarshal: %v", tc.name, err)
		}
		if !reflect.DeepEqual(back, tc.plan) {
			t.Errorf("%s: round trip = %+v; want %+v", tc.name, back, tc.plan)
		}
	}
}
