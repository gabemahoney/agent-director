package verdict_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/macfix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/verdict"
)

// TestCarriedOverFromGitHub judges the attempt in testdata/github-carry-over
// as GitHub served it: part installer, carried over from attempt 1, is listed
// under attempt 2 with run_attempt 2 and attempt 1's log. It is read as
// attempt 1's, from that attempt's artifact, and with the artifact gone, from
// the log; either way the attempt is complete and fails on b.up4's cases.
func TestCarriedOverFromGitHub(t *testing.T) {
	t.Parallel()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", "github-carry-over", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var attempt ghrun.Attempt
	var jobs struct {
		Jobs []ghrun.Job `json:"jobs"`
	}
	var rec model.Record
	for name, v := range map[string]any{"attempt2.json": &attempt, "attempt2-jobs.json": &jobs, "mac-part-installer-1-record.json": &rec} {
		if err := json.Unmarshal(read(name), v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// The tested commit's setup, and its selected tests, one darwin-only as there.
	root := treefix.Write(t, map[string]string{
		"test/mac/parts.txt":                              string(read("parts.txt")),
		"test/mac/expected-failures.txt":                  string(read("expected-failures.txt")),
		"test/install-sh/install_test.go":                 "package installsh_test\n\nimport \"testing\"\n\nfunc TestAgentCodexOnly(t *testing.T) {}\nfunc TestMoveSuccessful(t *testing.T) {}\n",
		"test/install-sh/move_clean_close_darwin_test.go": "package installsh_test\n\nimport \"testing\"\n\nfunc TestMoveAfterCleanClose(t *testing.T) {}\n",
	})
	r := &macfix.Reader{
		RunID: 43265158749, AttemptNumber: 2, HeadSHA: attempt.HeadSHA, HTMLURL: attempt.HTMLURL, JobList: jobs.Jobs,
		Records: map[string]ghrun.ArtifactFile{"mac-part-installer-1": {ArtifactID: 13965657049, Data: read("mac-part-installer-1-record.json")}},
		Logs:    map[int64][]byte{131348905644: read("job-131348905644.log")},
		Faults:  map[string]error{},
	}
	var cases []model.CaseResult
	for _, c := range rec.Cases {
		cases = append(cases, c.CaseResult)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Case < cases[j].Case })
	failed := func(id model.CaseID, marked bool) model.FailedCase {
		return model.FailedCase{Case: id, Part: "installer", Reason: model.ReasonFailed, Marked: marked}
	}
	// TestMoveAfterCleanClose fails for b.zsj, which the tested commit marks.
	wantFailed := []model.FailedCase{
		failed("go:test/install-sh:TestMoveAfterCleanClose", true),
		failed("go:test/install-sh:TestMoveSuccessful", false),
		failed("go:test/install-sh:TestMoveSuccessful/apple-sqlite3", false),
		failed("go:test/install-sh:TestMoveSuccessful/keep-prior", false),
		failed("go:test/install-sh:TestMoveSuccessful/plain", false),
	}
	for _, source := range []model.RecordSource{model.SourceArtifact, model.SourceLog} {
		want := model.PartVerdict{
			Name: "installer", Attempt: 1, Source: source, CarriedOver: true, Complete: true,
			LogURL:   "https://github.com/gabemahoney/agent-director/actions/runs/43265158749/job/131348905644",
			Versions: rec.Versions, Cases: cases,
		}
		if source == model.SourceArtifact {
			want.ArtifactURL = "https://github.com/gabemahoney/agent-director/actions/runs/43265158749/artifacts/13965657049"
		} else {
			delete(r.Records, "mac-part-installer-1")
		}
		opts := verdict.Options{
			Repo: "gabemahoney/agent-director", RunID: 43265158749, Attempt: 2, Root: root, Head: attempt.HeadSHA,
			ServerURL: "https://github.com", Out: filepath.Join(t.TempDir(), "out"),
		}
		got := runVerdict(t, opts, r)
		if got.code != 1 || got.v.Verdict != model.VerdictFailed || len(got.v.Reasons) != 0 || !reflect.DeepEqual(got.v.FailedCases, wantFailed) {
			t.Errorf("from the %s: Run = %d, %s, reasons %+v, failed %+v; want 1, failed, none, %+v",
				source, got.code, got.v.Verdict, got.v.Reasons, got.v.FailedCases, wantFailed)
		}
		if !reflect.DeepEqual(got.v.Parts, []model.PartVerdict{want}) {
			t.Errorf("from the %s: parts =\n%+v\nwant\n%+v", source, got.v.Parts, want)
		}
	}
}
