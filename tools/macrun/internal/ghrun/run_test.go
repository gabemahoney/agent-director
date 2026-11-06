package ghrun_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
)

// TestNames pins the names Task 3's workflow must give the part job and its
// cases step.
func TestNames(t *testing.T) {
	t.Parallel()
	if got := ghrun.PartJobName("api"); got != "part api" {
		t.Errorf("PartJobName = %q", got)
	}
	if ghrun.CasesStepName != "Run cases" {
		t.Errorf("CasesStepName = %q", ghrun.CasesStepName)
	}
}

func TestFailedSetupStep(t *testing.T) {
	t.Parallel()
	step := func(name string, number int, conclusion string) ghrun.Step {
		return ghrun.Step{Name: name, Number: number, Status: "completed", Conclusion: conclusion}
	}
	setUpGo := step("Set up Go", 2, "failure")
	for _, tc := range []struct {
		name  string
		steps []ghrun.Step
		want  ghrun.Step
		ok    bool
	}{
		{
			name:  "the first failed step by number, before Run cases",
			steps: []ghrun.Step{step("Run cases", 4, "skipped"), step("Install tmux", 3, "failure"), setUpGo, step("Set up job", 1, "success")},
			want:  setUpGo,
			ok:    true,
		},
		{
			name:  "only Run cases and a later step failed",
			steps: []ghrun.Step{step("Set up job", 1, "success"), step("Run cases", 2, "failure"), step("Upload record", 3, "failure")},
		},
		{
			name:  "no Run cases step listed",
			steps: []ghrun.Step{step("Set up job", 1, "success"), setUpGo},
			want:  setUpGo,
			ok:    true,
		},
		{
			name:  "no step failed",
			steps: []ghrun.Step{step("Set up job", 1, "success"), step("Run cases", 2, "success")},
		},
	} {
		if got, ok := ghrun.FailedSetupStep(tc.steps); got != tc.want || ok != tc.ok {
			t.Errorf("%s: FailedSetupStep = %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record := `{"schema_version":1,"part":"api"}`
	archive := zipOf(t, "other.txt", "x", "record.json", record)
	log := "2026-11-05T14:00:00.0000000Z @macrun-event {}\r\n"

	// Page 1 holds 100 jobs; page 2 holds one carried over from attempt 1.
	var page1 []string
	var wantJobs []ghrun.Job
	for i := 1; i <= 100; i++ {
		id := int64(1000 + i)
		url := fmt.Sprintf("https://github.com/o/r/actions/runs/7/job/%d", id)
		page1 = append(page1, fmt.Sprintf(`{"id":%d,"run_id":7,"name":"part p%d","run_attempt":2,"status":"completed",`+
			`"conclusion":"success","html_url":%q,"steps":[]}`, id, i, url))
		wantJobs = append(wantJobs, ghrun.Job{
			ID: id, Name: fmt.Sprintf("part p%d", i), RunAttempt: 2, Status: "completed", Conclusion: "success",
			HTMLURL: url, Steps: []ghrun.Step{},
		})
	}
	wantJobs = append(wantJobs, ghrun.Job{
		ID: 1101, Name: "part api", RunAttempt: 1, Status: "completed", Conclusion: "failure",
		HTMLURL: "https://github.com/o/r/actions/runs/7/job/1101",
		Steps: []ghrun.Step{
			{Name: "Set up job", Number: 1, Status: "completed", Conclusion: "success"},
			{Name: "Run cases", Number: 2, Status: "completed", Conclusion: "failure"},
		},
	})

	f := newFakeGitHub(t, map[string]response{
		repoURI:                 {body: `{"id":1,"name":"r","private":true}`},
		attemptURI:              {body: `{"id":7,"run_attempt":2,"head_sha":"c0ffee","html_url":"https://github.com/o/r/actions/runs/7/attempts/2"}`},
		fmt.Sprintf(jobsURI, 1): {body: `{"total_count":101,"jobs":[` + strings.Join(page1, ",") + `]}`},
		fmt.Sprintf(jobsURI, 2): {body: `{"total_count":101,"jobs":[{"id":1101,"run_id":7,"name":"part api","run_attempt":1,` +
			`"status":"completed","conclusion":"failure","html_url":"https://github.com/o/r/actions/runs/7/job/1101","steps":[` +
			`{"name":"Set up job","status":"completed","conclusion":"success","number":1,"started_at":"2026-11-05T14:00:00Z"},` +
			`{"name":"Run cases","status":"completed","conclusion":"failure","number":2}]}]}`},
		// 12 is newer but expired; 10 is the newest unexpired.
		artifactsURI: {body: `{"total_count":3,"artifacts":[{"id":9,"name":"mac-part-api-2","expired":false},` +
			`{"id":12,"name":"mac-part-api-2","expired":true},{"id":10,"name":"mac-part-api-2","expired":false}]}`},
		zipURI:                              {redirect: "/zip/10?sig=s3cret"},
		"/repos/o/r/actions/jobs/1101/logs": {redirect: "/logs/1101?sig=s3cret"},
	}, map[string]response{
		"/zip/10?sig=s3cret":    {body: string(archive)},
		"/logs/1101?sig=s3cret": {body: log},
	})
	c := f.client(t)
	// Each download is exactly at its cap.
	c.MaxArtifactBytes, c.MaxLogBytes = int64(len(archive)), int64(len(log))

	if private, err := c.RepositoryPrivate(ctx); err != nil || !private {
		t.Errorf("RepositoryPrivate = %v, %v; want true", private, err)
	}
	wantAttempt := ghrun.Attempt{HeadSHA: "c0ffee", HTMLURL: "https://github.com/o/r/actions/runs/7/attempts/2"}
	if got, err := c.Attempt(ctx, 7, 2); err != nil || got != wantAttempt {
		t.Errorf("Attempt = %+v, %v; want %+v", got, err, wantAttempt)
	}
	if got, err := c.Jobs(ctx, 7, 2); err != nil || !reflect.DeepEqual(got, wantJobs) {
		t.Errorf("Jobs = %d jobs, %v; want %d, the last %+v", len(got), err, len(wantJobs), wantJobs[100])
	}
	got, found, err := c.ArtifactFile(ctx, 7, "mac-part-api-2", "record.json")
	if err != nil || !found || got.ArtifactID != 10 || string(got.Data) != record {
		t.Errorf("ArtifactFile = %d, %q, found %v, %v; want 10, %q", got.ArtifactID, got.Data, found, err, record)
	}
	if got, found, err := c.JobLog(ctx, 1101); err != nil || !found || string(got) != log {
		t.Errorf("JobLog = %q, found %v, %v; want %q", got, found, err, log)
	}
	// The bytes came through the download server, whose handler checks that
	// no Authorization header reached it.
	for _, uri := range []string{"/zip/10?sig=s3cret", "/logs/1101?sig=s3cret"} {
		if n := f.hits(uri); n != 1 {
			t.Errorf("download %s asked %d times; want 1", uri, n)
		}
	}
}

func TestNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	artifactFile := func(c *ghrun.Client) (any, bool, error) {
		return c.ArtifactFile(ctx, 7, "mac-part-api-2", "record.json")
	}
	for _, tc := range []struct {
		name           string
		api, downloads map[string]response
		read           func(*ghrun.Client) (any, bool, error)
		want           any
	}{
		{
			name: "no artifact of the name",
			api:  map[string]response{artifactsURI: {body: `{"total_count":0,"artifacts":[]}`}},
			read: artifactFile,
			want: ghrun.ArtifactFile{},
		},
		{
			name: "only an expired artifact",
			api:  map[string]response{artifactsURI: {body: `{"total_count":1,"artifacts":[{"id":12,"name":"mac-part-api-2","expired":true}]}`}},
			read: artifactFile,
			want: ghrun.ArtifactFile{},
		},
		{
			name: "artifact listed, its zip gone",
			api:  map[string]response{artifactsURI: {body: oneArtifact}},
			read: artifactFile,
			want: ghrun.ArtifactFile{ArtifactID: 10},
		},
		{
			name:      "file not in the artifact",
			api:       map[string]response{artifactsURI: {body: oneArtifact}, zipURI: {redirect: "/zip/10?sig=s3cret"}},
			downloads: map[string]response{"/zip/10?sig=s3cret": {body: string(zipOf(t, "other.txt", "x"))}},
			read:      artifactFile,
			want:      ghrun.ArtifactFile{ArtifactID: 10},
		},
		{
			name: "no job log",
			read: func(c *ghrun.Client) (any, bool, error) { return c.JobLog(ctx, 9) },
			want: []byte(nil),
		},
	} {
		got, found, err := tc.read(newFakeGitHub(t, tc.api, tc.downloads).client(t))
		if err != nil || found || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read = %+v, found %v, %v; want %+v, not found, no error", tc.name, got, found, err, tc.want)
		}
	}
}
