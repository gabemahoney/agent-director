package model_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

const forms = "; use go:<package dir>:<test name>, go:<package dir> or testplan:<slug>:<t2 id>"

func TestCaseIDConstructors(t *testing.T) {
	t.Parallel()
	if got := model.GoTestCase("test/install-sh", "TestAgentCodexOnly"); got != "go:test/install-sh:TestAgentCodexOnly" {
		t.Errorf("GoTestCase = %q", got)
	}
	if got := model.GoPackageCase("test/install-sh"); got != "go:test/install-sh" {
		t.Errorf("GoPackageCase = %q", got)
	}
}

func TestParseCaseID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, wantErr string
	}{
		{in: "go:test/install-sh:TestAgentCodexOnly"},
		{in: "go:test/install-sh"},
		{in: "go:pkg/api:TestSpawn/relay_mode/off"},
		{in: "testplan:harness-smoke:t2.75s.ab.cd"},
		{in: "", wantErr: "case ID is empty" + forms},
		{in: "test/install-sh:TestA", wantErr: `case ID "test/install-sh:TestA" has no go: or testplan: prefix` + forms},
		{in: "go:pkg/a:Test\tA", wantErr: "case ID contains a tab or line break" + forms},
		{in: "go:./pkg/a:TestA", wantErr: `case ID "go:./pkg/a:TestA": package dir is not a clean relative path` + forms},
		{in: "go:pkg/a/:TestA", wantErr: `case ID "go:pkg/a/:TestA": package dir ends in /` + forms},
		{in: "go:/pkg/a", wantErr: `case ID "go:/pkg/a": package dir is absolute` + forms},
		{in: "go:", wantErr: `case ID "go:": package dir is empty` + forms},
		{in: "go:pkg a:TestA", wantErr: `case ID "go:pkg a:TestA": package dir contains a space` + forms},
		{in: "go:pkg/a:", wantErr: `case ID "go:pkg/a:" has an empty test name` + forms},
		{in: "testplan:harness-smoke", wantErr: `case ID "testplan:harness-smoke" is not of the form testplan:<slug>:<t2 id>` + forms},
	} {
		got, err := model.ParseCaseID(tc.in)
		if tc.wantErr == "" {
			if err != nil || got != model.CaseID(tc.in) {
				t.Errorf("ParseCaseID(%q) = %q, %v; want it accepted", tc.in, got, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.wantErr {
			t.Errorf("ParseCaseID(%q) error = %v; want %q", tc.in, err, tc.wantErr)
		}
	}
}
