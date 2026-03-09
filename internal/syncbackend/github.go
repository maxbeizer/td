package syncbackend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	tdsync "github.com/marcus/td/internal/sync"
)

// GitHubBackend implements SyncBackend using GitHub Issues as the remote store.
type GitHubBackend struct {
	owner string
	repo  string
	api   *ghAPI

	// issueCache maps td issue ID → GitHub issue number (populated lazily on push)
	issueCache map[string]int
}

// NewGitHubBackend creates a backend that syncs via GitHub Issues.
func NewGitHubBackend(owner, repo, token string) *GitHubBackend {
	return &GitHubBackend{
		owner:      owner,
		repo:       repo,
		api:        newGHAPI(owner, repo, token),
		issueCache: make(map[string]int),
	}
}

func (g *GitHubBackend) Name() string { return "github" }

// Push translates td sync events into GitHub Issue API calls.
func (g *GitHubBackend) Push(req *PushRequest) (*PushResult, error) {
	if err := g.api.ensureLabels(); err != nil {
		return nil, fmt.Errorf("ensure labels: %w", err)
	}

	// Build td_id → GitHub issue number cache from existing issues
	if len(g.issueCache) == 0 {
		if err := g.populateIssueCache(); err != nil {
			slog.Warn("github: populate issue cache", "err", err)
		}
	}

	result := &PushResult{}
	for _, ev := range req.Events {
		ack, err := g.pushEvent(ev)
		if err != nil {
			slog.Warn("github: push event failed", "entity", ev.EntityType, "id", ev.EntityID, "err", err)
			result.Rejected = append(result.Rejected, Rejection{
				ClientActionID: ev.ClientActionID,
				Reason:         err.Error(),
			})
			continue
		}
		result.Accepted++
		result.Acks = append(result.Acks, *ack)
	}
	return result, nil
}

func (g *GitHubBackend) pushEvent(ev tdsync.Event) (*Ack, error) {
	switch ev.EntityType {
	case "issues":
		return g.pushIssueEvent(ev)
	case "logs", "comments", "handoffs":
		return g.pushCommentEvent(ev)
	default:
		// Entity types we don't map to GitHub yet — acknowledge silently
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      time.Now().UnixMilli(),
		}, nil
	}
}

func (g *GitHubBackend) pushIssueEvent(ev tdsync.Event) (*Ack, error) {
	switch ev.ActionType {
	case "create", "update":
		return g.pushIssueCreateOrUpdate(ev)
	case "delete":
		return g.pushIssueDelete(ev)
	default:
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      time.Now().UnixMilli(),
		}, nil
	}
}

func (g *GitHubBackend) pushIssueCreateOrUpdate(ev tdsync.Event) (*Ack, error) {
	// The event payload is an envelope: {"schema_version":1,"new_data":{...},"previous_data":{...}}
	newData, err := extractNewData(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("extract new_data: %w", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(newData, &fields); err != nil {
		return nil, fmt.Errorf("unmarshal issue payload: %w", err)
	}

	tdID := ev.EntityID
	title, _ := fields["title"].(string)
	description, _ := fields["description"].(string)
	status, _ := fields["status"].(string)
	priority, _ := fields["priority"].(string)
	issueType, _ := fields["type"].(string)

	if title == "" {
		title = "(untitled)"
	}

	body := buildIssueBody(description, tdID, newData)
	labels := buildLabels(status, priority, issueType)

	ghState := "open"
	if status == "closed" {
		ghState = "closed"
	}

	// Check if this issue already exists on GitHub
	if ghNumber, ok := g.issueCache[tdID]; ok {
		// Update existing
		updated, err := g.api.updateIssue(ghNumber, title, body, labels, ghState)
		if err != nil {
			return nil, fmt.Errorf("update issue #%d: %w", ghNumber, err)
		}
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      updated.UpdatedAt.UnixMilli(),
		}, nil
	}

	// Create new
	created, err := g.api.createIssue(title, body, labels)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	g.issueCache[tdID] = created.Number
	return &Ack{
		ClientActionID: ev.ClientActionID,
		ServerSeq:      created.UpdatedAt.UnixMilli(),
	}, nil
}

func (g *GitHubBackend) pushIssueDelete(ev tdsync.Event) (*Ack, error) {
	tdID := ev.EntityID
	ghNumber, ok := g.issueCache[tdID]
	if !ok {
		// Nothing to close — might not exist on GitHub
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      time.Now().UnixMilli(),
		}, nil
	}

	updated, err := g.api.updateIssue(ghNumber, "", "", nil, "closed")
	if err != nil {
		return nil, fmt.Errorf("close issue #%d: %w", ghNumber, err)
	}
	return &Ack{
		ClientActionID: ev.ClientActionID,
		ServerSeq:      updated.UpdatedAt.UnixMilli(),
	}, nil
}

func (g *GitHubBackend) pushCommentEvent(ev tdsync.Event) (*Ack, error) {
	// Extract new_data from the envelope
	newData, err := extractNewData(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("extract new_data: %w", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(newData, &fields); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	issueID, _ := fields["issue_id"].(string)
	if issueID == "" {
		// Can't post comment without a parent issue
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      time.Now().UnixMilli(),
		}, nil
	}

	ghNumber, ok := g.issueCache[issueID]
	if !ok {
		// Parent issue not on GitHub — skip silently
		return &Ack{
			ClientActionID: ev.ClientActionID,
			ServerSeq:      time.Now().UnixMilli(),
		}, nil
	}

	commentBody := formatCommentBody(ev.EntityType, fields)
	if err := g.api.createComment(ghNumber, commentBody); err != nil {
		return nil, fmt.Errorf("create comment on #%d: %w", ghNumber, err)
	}

	return &Ack{
		ClientActionID: ev.ClientActionID,
		ServerSeq:      time.Now().UnixMilli(),
	}, nil
}

// Pull fetches GitHub Issues updated since afterSeq (interpreted as Unix millis)
// and converts them to sync Events.
func (g *GitHubBackend) Pull(afterSeq int64, limit int, excludeDeviceID string) (*PullResult, error) {
	var since time.Time
	if afterSeq > 0 {
		since = time.UnixMilli(afterSeq)
	}

	allIssues, err := g.api.listAllIssues(since)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	result := &PullResult{}
	for _, issue := range allIssues {
		tdID := parseTdID(issue.Body)
		if tdID == "" {
			continue // Not a td-managed issue
		}

		// Try to use stored payload for full fidelity
		payload := parseStoredPayload(issue.Body)
		if payload == nil {
			// Synthesize payload from GitHub Issue fields
			payload = synthesizePayload(issue, tdID)
		} else {
			// Update payload with current GitHub state (labels may have changed)
			payload = mergeGitHubState(payload, issue)
		}

		// Wrap payload in the envelope format expected by ApplyRemoteEvents
		wrappedPayload := wrapPayload(payload)

		ev := tdsync.Event{
			ServerSeq:       issue.UpdatedAt.UnixMilli(),
			DeviceID:        "github",
			SessionID:       "github-sync",
			ClientActionID:  int64(issue.Number),
			ActionType:      "create", // use "create" for full upsert (works for both new and existing)
			EntityType:      "issues",
			EntityID:        tdID,
			Payload:         wrappedPayload,
			ClientTimestamp: issue.UpdatedAt,
		}

		result.Events = append(result.Events, ev)
		if issue.UpdatedAt.UnixMilli() > result.LastServerSeq {
			result.LastServerSeq = issue.UpdatedAt.UnixMilli()
		}
	}

	// Respect limit
	if limit > 0 && len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.HasMore = true
		if len(result.Events) > 0 {
			result.LastServerSeq = result.Events[len(result.Events)-1].ServerSeq
		}
	}

	return result, nil
}

// Status returns sync status from GitHub.
func (g *GitHubBackend) Status() (*SyncStatus, error) {
	issues, err := g.api.listAllIssues(time.Time{})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	var lastSeq int64
	var lastTime string
	for _, issue := range issues {
		ms := issue.UpdatedAt.UnixMilli()
		if ms > lastSeq {
			lastSeq = ms
			lastTime = issue.UpdatedAt.Format(time.RFC3339)
		}
	}

	return &SyncStatus{
		EventCount:    int64(len(issues)),
		LastServerSeq: lastSeq,
		LastEventTime: lastTime,
	}, nil
}

// populateIssueCache fetches all td:managed issues and builds the td_id → number map.
func (g *GitHubBackend) populateIssueCache() error {
	issues, err := g.api.listAllIssues(time.Time{})
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if tdID := parseTdID(issue.Body); tdID != "" {
			g.issueCache[tdID] = issue.Number
		}
	}
	slog.Debug("github: populated issue cache", "count", len(g.issueCache))
	return nil
}

// getGitHubToken retrieves a GitHub token from env vars or gh CLI.
func getGitHubToken() (string, error) {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no GitHub token found (set GITHUB_TOKEN or run 'gh auth login')")
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gh auth token returned empty (run 'gh auth login')")
	}
	return token, nil
}
