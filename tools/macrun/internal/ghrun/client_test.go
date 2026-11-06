package ghrun_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
)

const token = "ghs_fakeRunToken42"

// The API request URIs the reads use, for run 7, attempt 2 and job 9.
const (
	repoURI      = "/repos/o/r"
	attemptURI   = "/repos/o/r/actions/runs/7/attempts/2"
	jobsURI      = attemptURI + "/jobs?page=%d&per_page=100"
	artifactsURI = "/repos/o/r/actions/runs/7/artifacts?name=mac-part-api-2&per_page=100"
	zipURI       = "/repos/o/r/actions/artifacts/10/zip"
	logURI       = "/repos/o/r/actions/jobs/9/logs"
	oneArtifact  = `{"total_count":1,"artifacts":[{"id":10,"name":"mac-part-api-2","expired":false}]}`
)

// response is a fake server's answer to one request URI.
type response struct {
	// status defaults to 200.
	status int
	body   string
	// redirect is a download server URI to redirect to.
	redirect string
	// drop closes the connection without an answer.
	drop bool
}

// fakeGitHub is an API server and a download server under another host
// name, answering from route tables keyed by request URI (404 otherwise),
// checking every request's headers and counting each URI's requests.
type fakeGitHub struct {
	api   *httptest.Server
	mu    sync.Mutex
	count map[string]int
}

func newFakeGitHub(t *testing.T, api, downloads map[string]response) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{count: map[string]int{}}
	var downloadsURL string
	serve := func(routes map[string]response, check func(*http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			check(r)
			f.mu.Lock()
			f.count[r.URL.RequestURI()]++
			f.mu.Unlock()
			resp, ok := routes[r.URL.RequestURI()]
			switch {
			case !ok:
				http.NotFound(w, r)
			case resp.drop:
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					conn.Close()
				}
			case resp.redirect != "":
				http.Redirect(w, r, downloadsURL+resp.redirect, http.StatusFound)
			default:
				if resp.status != 0 {
					w.WriteHeader(resp.status)
				}
				io.WriteString(w, resp.body)
			}
		}
	}
	dl := httptest.NewServer(serve(downloads, func(r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("download request %s carried Authorization %q", r.URL, got)
		}
	}))
	t.Cleanup(dl.Close)
	// Go's client keeps Authorization across a redirect to another port of
	// the same host, so downloads go to another host name, as GitHub's do.
	downloadsURL = strings.Replace(dl.URL, "127.0.0.1", "localhost", 1)
	if downloadsURL == dl.URL {
		t.Fatalf("download server %s isn't on 127.0.0.1", dl.URL)
	}
	f.api = httptest.NewServer(serve(api, func(r *http.Request) {
		for name, want := range map[string]string{
			"Authorization":        "Bearer " + token,
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("API request %s: %s = %q; want %q", r.URL, name, got, want)
			}
		}
	}))
	t.Cleanup(f.api.Close)
	return f
}

// client returns a client of f's API with no retry backoff.
func (f *fakeGitHub) client(t *testing.T) *ghrun.Client {
	t.Helper()
	c, err := ghrun.NewClient(f.api.URL, "o/r", token)
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = 0
	return c
}

// hits returns how many requests asked for uri.
func (f *fakeGitHub) hits(uri string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count[uri]
}

// zipOf returns a zip archive of the named files, given as name, content
// pairs.
func zipOf(t *testing.T, files ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for i := 0; i < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, files[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	c, err := ghrun.NewClient("https://api.github.com", "o/r", token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backoff != ghrun.DefaultBackoff || c.MaxArtifactBytes != ghrun.DefaultMaxArtifactBytes || c.MaxLogBytes != ghrun.DefaultMaxLogBytes {
		t.Errorf("NewClient settings = %v, %d, %d; want the defaults", c.Backoff, c.MaxArtifactBytes, c.MaxLogBytes)
	}
	for _, repo := range []string{"o", "o/", "/r", "o/r/x"} {
		want := fmt.Sprintf("repository %q is not of the form owner/name", repo)
		if _, err := ghrun.NewClient("https://api.github.com", repo, token); err == nil || err.Error() != want {
			t.Errorf("NewClient(%q) error = %v; want %q", repo, err, want)
		}
	}
}

func TestFaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	attempt := func(c *ghrun.Client) error { _, err := c.Attempt(ctx, 7, 2); return err }
	artifactFile := func(c *ghrun.Client) error {
		_, _, err := c.ArtifactFile(ctx, 7, "mac-part-api-2", "record.json")
		return err
	}
	jobLog := func(c *ghrun.Client) error { _, _, err := c.JobLog(ctx, 9); return err }
	archive := zipOf(t, "record.json", "{}")
	// The file inflates past a cap its zip is under.
	bomb := zipOf(t, "record.json", strings.Repeat("a", 4096))
	exactly := func(s string) string { return "^" + regexp.QuoteMeta(s) + "$" }
	for _, tc := range []struct {
		name           string
		api, downloads map[string]response
		caps           func(*ghrun.Client)
		read           func(*ghrun.Client) error
		// uri is the API request URI asked hits times.
		uri  string
		hits int
		// wantErr matches the whole error.
		wantErr string
	}{
		{
			name:    "5xx retried, then reported",
			api:     map[string]response{attemptURI: {status: http.StatusBadGateway}},
			read:    attempt,
			uri:     attemptURI,
			hits:    4,
			wantErr: exactly("GET " + attemptURI + ": HTTP 502 (tried 4 times)"),
		},
		{
			name: "4xx not retried, an echoed token blanked",
			api: map[string]response{fmt.Sprintf(jobsURI, 1): {
				status: http.StatusForbidden,
				body:   `{"message":"token ` + token + ` lacks actions: read"}`,
			}},
			read:    func(c *ghrun.Client) error { _, err := c.Jobs(ctx, 7, 2); return err },
			uri:     fmt.Sprintf(jobsURI, 1),
			hits:    1,
			wantErr: exactly(`GET /repos/o/r/actions/runs/7/attempts/2/jobs: HTTP 403: {"message":"token [token] lacks actions: read"}`),
		},
		{
			name:    "repository answer without its visibility",
			api:     map[string]response{repoURI: {body: `{"id":1,"name":"r"}`}},
			read:    func(c *ghrun.Client) error { _, err := c.RepositoryPrivate(ctx); return err },
			uri:     repoURI,
			hits:    1,
			wantErr: exactly("GET /repos/o/r: response has no private field"),
		},
		{
			name:      "log over its cap",
			api:       map[string]response{logURI: {redirect: "/logs/9?sig=s3cret"}},
			downloads: map[string]response{"/logs/9?sig=s3cret": {body: "123456789"}},
			caps:      func(c *ghrun.Client) { c.MaxLogBytes = 8 },
			read:      jobLog,
			uri:       logURI,
			hits:      1,
			wantErr:   exactly("GET " + logURI + ": response passes its size cap of 8 bytes"),
		},
		{
			name:      "artifact zip over its cap",
			api:       map[string]response{artifactsURI: {body: oneArtifact}, zipURI: {redirect: "/zip/10?sig=s3cret"}},
			downloads: map[string]response{"/zip/10?sig=s3cret": {body: string(archive)}},
			caps:      func(c *ghrun.Client) { c.MaxArtifactBytes = int64(len(archive) - 1) },
			read:      artifactFile,
			uri:       zipURI,
			hits:      1,
			wantErr:   exactly(fmt.Sprintf("GET %s: response passes its size cap of %d bytes", zipURI, len(archive)-1)),
		},
		{
			name:      "file in the artifact over its cap",
			api:       map[string]response{artifactsURI: {body: oneArtifact}, zipURI: {redirect: "/zip/10?sig=s3cret"}},
			downloads: map[string]response{"/zip/10?sig=s3cret": {body: string(bomb)}},
			caps:      func(c *ghrun.Client) { c.MaxArtifactBytes = int64(len(bomb)) },
			read:      artifactFile,
			uri:       zipURI,
			hits:      1,
			wantErr:   exactly(fmt.Sprintf("GET %s: record.json in artifact zip: response passes its size cap of %d bytes", zipURI, len(bomb))),
		},
		{
			// The redirect's URL is itself a credential, so the transport's
			// error text appears without it (no ?sig=).
			name:      "download connection dropped, retried, then reported",
			api:       map[string]response{logURI: {redirect: "/logs/9?sig=s3cret"}},
			downloads: map[string]response{"/logs/9?sig=s3cret": {drop: true}},
			read:      jobLog,
			uri:       logURI,
			hits:      4,
			wantErr:   "^" + regexp.QuoteMeta("GET "+logURI+": ") + `[^?]+ \(tried 4 times\)$`,
		},
	} {
		f := newFakeGitHub(t, tc.api, tc.downloads)
		c := f.client(t)
		if tc.caps != nil {
			tc.caps(c)
		}
		err := tc.read(c)
		if err == nil || !regexp.MustCompile(tc.wantErr).MatchString(err.Error()) {
			t.Errorf("%s: error = %v; want it to match %s", tc.name, err, tc.wantErr)
		} else if strings.Contains(err.Error(), token) {
			t.Errorf("%s: error %q contains the token", tc.name, err)
		}
		if got := f.hits(tc.uri); got != tc.hits {
			t.Errorf("%s: %s asked %d times; want %d", tc.name, tc.uri, got, tc.hits)
		}
	}
}
