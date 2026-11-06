// Package ghrun is macrun's GitHub client: the one place macrun talks to
// GitHub. It reads a workflow run's attempt, jobs, step conclusions, part
// artifacts and job logs through the GitHub REST API with the run's own token,
// and returns bytes for the result model to parse. It is reusable: macrun
// code that reads GitHub must use it, adding any read it needs to Client and
// Reader, never a second GitHub client.
package ghrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiVersion pins the REST API version every request asks for.
const apiVersion = "2022-11-28"

// requestTimeout bounds one request, its body included.
const requestTimeout = 2 * time.Minute

// maxTries is how many times a request is tried when the transport fails or
// GitHub answers 5xx.
const maxTries = 4

// DefaultBackoff is the wait before a request's first retry; each later
// retry waits one more of it.
const DefaultBackoff = 2 * time.Second

// DefaultMaxArtifactBytes caps an artifact's zip and the file read from it:
// far above a full-scope part's record, far below the job's memory.
const DefaultMaxArtifactBytes = 512 << 20

// DefaultMaxLogBytes caps a job's log: far above a full-scope part's verbose
// log, far below the job's memory.
const DefaultMaxLogBytes = 512 << 20

// Client reads one repository's workflow runs.
type Client struct {
	apiURL string
	owner  string
	repo   string
	token  string
	http   *http.Client

	// Backoff is the wait before a request's first retry. NewClient sets
	// DefaultBackoff.
	Backoff time.Duration
	// MaxArtifactBytes caps an artifact's zip and the file read from it.
	// NewClient sets DefaultMaxArtifactBytes.
	MaxArtifactBytes int64
	// MaxLogBytes caps a job's log. NewClient sets DefaultMaxLogBytes.
	MaxLogBytes int64
}

// NewClient returns a client of the REST API at apiURL (github.api_url) for
// the repository repo, owner/name, authenticated by token. Downloads follow
// GitHub's redirect to another host, and Go's client sends the token to no
// other host.
func NewClient(apiURL, repo, token string) (*Client, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("repository %q is not of the form owner/name", repo)
	}
	if _, err := url.Parse(apiURL); err != nil {
		return nil, fmt.Errorf("API URL %q doesn't parse: %w", apiURL, err)
	}
	return &Client{
		apiURL:           strings.TrimSuffix(apiURL, "/"),
		owner:            owner,
		repo:             name,
		token:            token,
		http:             &http.Client{Timeout: requestTimeout},
		Backoff:          DefaultBackoff,
		MaxArtifactBytes: DefaultMaxArtifactBytes,
		MaxLogBytes:      DefaultMaxLogBytes,
	}, nil
}

// repoPath returns the API path of the repository followed by elems, each
// escaped.
func (c *Client) repoPath(elems ...string) string {
	parts := []string{"repos", url.PathEscape(c.owner), url.PathEscape(c.repo)}
	for _, e := range elems {
		parts = append(parts, url.PathEscape(e))
	}
	return "/" + strings.Join(parts, "/")
}

// errNotFound is get's answer to a 404.
var errNotFound = errors.New("not found")

// get requests the API path with query and returns its body, read up to limit
// bytes. A body over limit is an error. A 404 is errNotFound. Errors name the
// endpoint and the status, never the token or a redirect's URL.
func (c *Client) get(ctx context.Context, path string, query url.Values, limit int64) ([]byte, error) {
	endpoint := "GET " + path
	target := c.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var lastErr error
	for try := 1; try <= maxTries; try++ {
		if try > 1 {
			if err := sleep(ctx, time.Duration(try-1)*c.Backoff); err != nil {
				return nil, fmt.Errorf("%s: %w", endpoint, err)
			}
		}
		body, status, err := c.do(ctx, target, limit)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("%s: %w", endpoint, err)
			if errors.Is(err, errTooLarge) || ctx.Err() != nil {
				return nil, lastErr
			}
		case status == http.StatusNotFound:
			return nil, fmt.Errorf("%s: %w", endpoint, errNotFound)
		case status >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d", endpoint, status)
		case status != http.StatusOK:
			return nil, fmt.Errorf("%s: HTTP %d: %s", endpoint, status, c.apiMessage(body))
		default:
			return body, nil
		}
	}
	return nil, fmt.Errorf("%w (tried %d times)", lastErr, maxTries)
}

// errTooLarge says a body passed its cap.
var errTooLarge = errors.New("response passes its size cap")

// do sends one request and reads its body up to limit bytes.
func (c *Client) do(ctx context.Context, target string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, withoutURL(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", withoutURL(err))
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, fmt.Errorf("%w of %d bytes", errTooLarge, limit)
	}
	return body, resp.StatusCode, nil
}

// withoutURL drops the URL from a transport error, because a download's
// redirect URL is itself a short-lived credential.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// apiMessage returns a short form of an error body, for an error message,
// with the token blanked should a server echo it.
func (c *Client) apiMessage(body []byte) string {
	const maxMessage = 200
	msg := string(bytes.TrimSpace(body))
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "[token]")
	}
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "…"
	}
	return msg
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
