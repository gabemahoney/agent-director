package ghrun

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
)

// The naming contract with the Mac workflow: its part job for a part is
// named PartJobName(part), and the step that runs macrun part is named
// CasesStepName. A workflow that names them otherwise loses its parts.
const (
	partJobPrefix = "part "
	// CasesStepName is the name of the part job's step that runs macrun part.
	CasesStepName = "Run cases"
)

// PartJobName returns the name of the part job that runs part.
func PartJobName(part string) string {
	return partJobPrefix + part
}

// jobsPerPage is the page size of the jobs listing, GitHub's largest.
const jobsPerPage = 100

// metadataLimit caps a JSON API response, which is far smaller.
const metadataLimit = 16 << 20

// Reader is what macrun verdict reads of a run. Client implements it; tests
// fake it.
type Reader interface {
	// RepositoryPrivate reports whether the repository is private.
	RepositoryPrivate(ctx context.Context) (bool, error)
	// Attempt reads one attempt of a run.
	Attempt(ctx context.Context, runID int64, attempt int) (Attempt, error)
	// Jobs lists every job of one attempt of a run.
	Jobs(ctx context.Context, runID int64, attempt int) ([]Job, error)
	// ArtifactFile reads the file named file from the run's unexpired
	// artifact named name. found is false when there is no such artifact or
	// file.
	ArtifactFile(ctx context.Context, runID int64, name, file string) (f ArtifactFile, found bool, err error)
	// JobLog reads a job's log. found is false when GitHub has none.
	JobLog(ctx context.Context, jobID int64) (log []byte, found bool, err error)
}

var _ Reader = (*Client)(nil)

// Attempt is one attempt of a workflow run.
type Attempt struct {
	// HeadSHA is the tested commit.
	HeadSHA string `json:"head_sha"`
	// HTMLURL links the attempt.
	HTMLURL string `json:"html_url"`
}

// Job is one job of a run attempt.
type Job struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// RunAttempt is the job's attempt, as GitHub returns it. When only
	// failed jobs were re-run, GitHub was seen (run 43265158749,
	// 2026-11-12) to list each job it carried over under the new attempt,
	// with that attempt's run_attempt and a new ID, but with the earlier
	// job's start and end times and log; GitHub's REST documentation for
	// "List jobs for a workflow run attempt" doesn't say. A value earlier
	// than the listed attempt is still read as the job carried over from
	// that attempt.
	RunAttempt int    `json:"run_attempt"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	Steps      []Step `json:"steps"`
}

// Step is one step of a job.
type Step struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ArtifactFile is one file read from an artifact.
type ArtifactFile struct {
	// ArtifactID is the artifact's ID, for a link to it.
	ArtifactID int64
	// Data is the file's content.
	Data []byte
}

// failure is a step's conclusion when it failed.
const failure = "failure"

// FailedSetupStep returns the step of a part job that failed before the
// cases step: the first step, by number, that concluded failure before
// CasesStepName, or any failed step when the job lists no cases step. ok is
// false when there is none, including when only the cases step or a later
// one failed.
func FailedSetupStep(steps []Step) (step Step, ok bool) {
	ordered := append([]Step(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Number < ordered[j].Number })
	for _, s := range ordered {
		if s.Name == CasesStepName {
			return Step{}, false
		}
		if s.Conclusion == failure {
			return s, true
		}
	}
	return Step{}, false
}

// RepositoryPrivate reads GET /repos/{owner}/{repo}.
func (c *Client) RepositoryPrivate(ctx context.Context) (bool, error) {
	var repo struct {
		Private *bool `json:"private"`
	}
	path := c.repoPath()
	if err := c.getJSON(ctx, path, nil, &repo); err != nil {
		return false, err
	}
	if repo.Private == nil {
		return false, fmt.Errorf("GET %s: response has no private field", path)
	}
	return *repo.Private, nil
}

// Attempt reads GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{n}.
func (c *Client) Attempt(ctx context.Context, runID int64, attempt int) (Attempt, error) {
	var a Attempt
	path := c.repoPath("actions", "runs", strconv.FormatInt(runID, 10), "attempts", strconv.Itoa(attempt))
	if err := c.getJSON(ctx, path, nil, &a); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

// Jobs reads every page of
// GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{n}/jobs.
func (c *Client) Jobs(ctx context.Context, runID int64, attempt int) ([]Job, error) {
	path := c.repoPath("actions", "runs", strconv.FormatInt(runID, 10), "attempts", strconv.Itoa(attempt), "jobs")
	var jobs []Job
	for page := 1; ; page++ {
		var resp struct {
			TotalCount int   `json:"total_count"`
			Jobs       []Job `json:"jobs"`
		}
		query := url.Values{"per_page": {strconv.Itoa(jobsPerPage)}, "page": {strconv.Itoa(page)}}
		if err := c.getJSON(ctx, path, query, &resp); err != nil {
			return nil, err
		}
		jobs = append(jobs, resp.Jobs...)
		if len(resp.Jobs) < jobsPerPage || len(jobs) >= resp.TotalCount {
			return jobs, nil
		}
	}
}

// artifact is one entry of the run's artifacts listing.
type artifact struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Expired bool   `json:"expired"`
}

// ArtifactFile lists the run's artifacts named name
// (GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts?name=…), takes
// the newest unexpired one, downloads its zip
// (GET /repos/{owner}/{repo}/actions/artifacts/{id}/zip) and reads file from
// it.
func (c *Client) ArtifactFile(ctx context.Context, runID int64, name, file string) (ArtifactFile, bool, error) {
	var resp struct {
		Artifacts []artifact `json:"artifacts"`
	}
	path := c.repoPath("actions", "runs", strconv.FormatInt(runID, 10), "artifacts")
	query := url.Values{"name": {name}, "per_page": {"100"}}
	if err := c.getJSON(ctx, path, query, &resp); err != nil {
		return ArtifactFile{}, false, err
	}
	var newest *artifact
	for i, a := range resp.Artifacts {
		if a.Name == name && !a.Expired && (newest == nil || a.ID > newest.ID) {
			newest = &resp.Artifacts[i]
		}
	}
	if newest == nil {
		return ArtifactFile{}, false, nil
	}
	zipPath := c.repoPath("actions", "artifacts", strconv.FormatInt(newest.ID, 10), "zip")
	archive, err := c.get(ctx, zipPath, nil, c.MaxArtifactBytes)
	if errors.Is(err, errNotFound) {
		return ArtifactFile{ArtifactID: newest.ID}, false, nil
	}
	if err != nil {
		return ArtifactFile{}, false, err
	}
	data, found, err := c.readZipFile(archive, file)
	if err != nil {
		return ArtifactFile{}, false, fmt.Errorf("GET %s: %w", zipPath, err)
	}
	return ArtifactFile{ArtifactID: newest.ID, Data: data}, found, nil
}

// readZipFile reads the file named name from a zip archive, up to
// MaxArtifactBytes.
func (c *Client) readZipFile(archive []byte, name string) ([]byte, bool, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, false, fmt.Errorf("artifact zip doesn't open: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, false, fmt.Errorf("open %s in artifact zip: %w", name, err)
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, c.MaxArtifactBytes+1))
		if err != nil {
			return nil, false, fmt.Errorf("read %s in artifact zip: %w", name, err)
		}
		if int64(len(data)) > c.MaxArtifactBytes {
			return nil, false, fmt.Errorf("%s in artifact zip: %w of %d bytes", name, errTooLarge, c.MaxArtifactBytes)
		}
		return data, true, nil
	}
	return nil, false, nil
}

// JobLog reads GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs.
func (c *Client) JobLog(ctx context.Context, jobID int64) ([]byte, bool, error) {
	path := c.repoPath("actions", "jobs", strconv.FormatInt(jobID, 10), "logs")
	log, err := c.get(ctx, path, nil, c.MaxLogBytes)
	if errors.Is(err, errNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return log, true, nil
}

// getJSON requests an API path and decodes its JSON body into v.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, v any) error {
	body, err := c.get(ctx, path, query, metadataLimit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: response doesn't parse: %w", path, err)
	}
	return nil
}
