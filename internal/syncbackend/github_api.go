package syncbackend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	githubAPIBase    = "https://api.github.com"
	labelTdManaged   = "td:managed"
	labelPrefix      = "td:"
)

// ghIssue is the GitHub Issue API response.
type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []ghLabel `json:"labels"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// ghLabel is the GitHub Label API response.
type ghLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// ghAPI wraps GitHub REST API calls.
type ghAPI struct {
	owner   string
	repo    string
	token   string
	http    *http.Client
	baseURL string // overridable for testing

	labelsEnsured bool
}

func newGHAPI(owner, repo, token string) *ghAPI {
	return &ghAPI{
		owner:   owner,
		repo:    repo,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: githubAPIBase,
	}
}

func (a *ghAPI) createIssue(title, body string, labels []string) (*ghIssue, error) {
	payload := map[string]any{
		"title":  title,
		"body":   body,
		"labels": labels,
	}
	var result ghIssue
	if err := a.post(fmt.Sprintf("/repos/%s/%s/issues", a.owner, a.repo), payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *ghAPI) updateIssue(number int, title, body string, labels []string, state string) (*ghIssue, error) {
	payload := make(map[string]any)
	if title != "" {
		payload["title"] = title
	}
	if body != "" {
		payload["body"] = body
	}
	if labels != nil {
		payload["labels"] = labels
	}
	if state != "" {
		payload["state"] = state
	}
	var result ghIssue
	if err := a.patch(fmt.Sprintf("/repos/%s/%s/issues/%d", a.owner, a.repo, number), payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *ghAPI) createComment(issueNumber int, body string) error {
	payload := map[string]any{"body": body}
	return a.post(fmt.Sprintf("/repos/%s/%s/issues/%d/comments", a.owner, a.repo, issueNumber), payload, nil)
}

// listAllIssues fetches all td:managed issues, paginating automatically.
func (a *ghAPI) listAllIssues(since time.Time) ([]ghIssue, error) {
	var all []ghIssue
	page := 1
	for {
		batch, err := a.listIssues(since, page)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		page++
	}
	return all, nil
}

func (a *ghAPI) listIssues(since time.Time, page int) ([]ghIssue, error) {
	params := url.Values{
		"labels":    {labelTdManaged},
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"asc"},
		"per_page":  {"100"},
		"page":      {fmt.Sprintf("%d", page)},
	}
	if !since.IsZero() {
		params.Set("since", since.UTC().Format(time.RFC3339))
	}

	path := fmt.Sprintf("/repos/%s/%s/issues?%s", a.owner, a.repo, params.Encode())
	var issues []ghIssue
	if err := a.get(path, &issues); err != nil {
		return nil, err
	}

	// Filter out pull requests (GitHub API returns PRs in the issues endpoint)
	filtered := issues[:0]
	for _, issue := range issues {
		if !strings.Contains(fmt.Sprintf("%v", issue), "pull_request") {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

type labelDef struct {
	Name        string
	Color       string
	Description string
}

var tdLabelDefs = []labelDef{
	{labelTdManaged, "666666", "Managed by td sync"},
	// Status
	{"td:open", "0e8a16", "td status: open"},
	{"td:in-progress", "fbca04", "td status: in progress"},
	{"td:in-review", "5319e7", "td status: in review"},
	{"td:blocked", "d73a4a", "td status: blocked"},
	// Priority
	{"td:p0", "b60205", "td priority: critical"},
	{"td:p1", "d93f0b", "td priority: high"},
	{"td:p2", "fbca04", "td priority: medium"},
	{"td:p3", "0075ca", "td priority: low"},
	// Type
	{"td:task", "c5def5", "td type: task"},
	{"td:bug", "d73a4a", "td type: bug"},
	{"td:feature", "a2eeef", "td type: feature"},
	{"td:epic", "7057ff", "td type: epic"},
}

// ensureLabels creates any missing td: labels in the GitHub repo.
func (a *ghAPI) ensureLabels() error {
	if a.labelsEnsured {
		return nil
	}

	// Fetch existing labels
	existing := make(map[string]bool)
	page := 1
	for {
		var labels []ghLabel
		if err := a.get(fmt.Sprintf("/repos/%s/%s/labels?per_page=100&page=%d", a.owner, a.repo, page), &labels); err != nil {
			return fmt.Errorf("list labels: %w", err)
		}
		for _, l := range labels {
			existing[l.Name] = true
		}
		if len(labels) < 100 {
			break
		}
		page++
	}

	// Create missing labels
	for _, def := range tdLabelDefs {
		if existing[def.Name] {
			continue
		}
		payload := map[string]string{
			"name":        def.Name,
			"color":       def.Color,
			"description": def.Description,
		}
		if err := a.post(fmt.Sprintf("/repos/%s/%s/labels", a.owner, a.repo), payload, nil); err != nil {
			// 422 means label already exists (race condition) — that's fine
			if !strings.Contains(err.Error(), "422") {
				return fmt.Errorf("create label %q: %w", def.Name, err)
			}
		}
		slog.Debug("github: created label", "name", def.Name)
	}

	a.labelsEnsured = true
	return nil
}

// HTTP helpers

func (a *ghAPI) get(path string, result any) error {
	return a.doRequest("GET", path, nil, result)
}

func (a *ghAPI) post(path string, body, result any) error {
	return a.doRequest("POST", path, body, result)
}

func (a *ghAPI) patch(path string, body, result any) error {
	return a.doRequest("PATCH", path, body, result)
}

func (a *ghAPI) doRequest(method, path string, body, result any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	fullURL := a.baseURL + path
	req, err := http.NewRequest(method, fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Check rate limits
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		reset := resp.Header.Get("X-RateLimit-Reset")
		return fmt.Errorf("github rate limited (remaining: %s, reset: %s)", remaining, reset)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("github API %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}
