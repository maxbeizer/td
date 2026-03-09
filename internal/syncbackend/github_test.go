package syncbackend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tdsync "github.com/marcus/td/internal/sync"
)

func TestGitHubBackend_ImplementsInterface(t *testing.T) {
	var _ SyncBackend = (*GitHubBackend)(nil)
}

func TestGitHubBackend_Name(t *testing.T) {
	b := NewGitHubBackend("owner", "repo", "token")
	if b.Name() != "github" {
		t.Errorf("Name() = %q, want %q", b.Name(), "github")
	}
}

// fakeGitHubServer creates a mock GitHub API server for testing.
func fakeGitHubServer(t *testing.T) (*httptest.Server, *ghServerState) {
	t.Helper()
	state := &ghServerState{
		issues: make(map[int]*ghIssue),
		labels: make(map[string]bool),
	}

	mux := http.NewServeMux()

	// POST /repos/{owner}/{repo}/issues — create issue
	mux.HandleFunc("POST /repos/testowner/testrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		state.nextNumber++
		now := time.Now()
		issue := &ghIssue{
			Number:    state.nextNumber,
			Title:     req.Title,
			Body:      req.Body,
			State:     "open",
			CreatedAt: now,
			UpdatedAt: now,
		}
		for _, l := range req.Labels {
			issue.Labels = append(issue.Labels, ghLabel{Name: l})
		}
		state.issues[issue.Number] = issue

		w.WriteHeader(201)
		json.NewEncoder(w).Encode(issue)
	})

	// PATCH /repos/{owner}/{repo}/issues/{number} — update issue
	mux.HandleFunc("PATCH /repos/testowner/testrepo/issues/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		var num int
		fmt.Sscanf(parts[len(parts)-1], "%d", &num)

		issue, ok := state.issues[num]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}

		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		if title, ok := req["title"].(string); ok && title != "" {
			issue.Title = title
		}
		if body, ok := req["body"].(string); ok && body != "" {
			issue.Body = body
		}
		if s, ok := req["state"].(string); ok && s != "" {
			issue.State = s
		}
		if labels, ok := req["labels"].([]any); ok {
			issue.Labels = nil
			for _, l := range labels {
				if name, ok := l.(string); ok {
					issue.Labels = append(issue.Labels, ghLabel{Name: name})
				}
			}
		}
		issue.UpdatedAt = time.Now()

		json.NewEncoder(w).Encode(issue)
	})

	// GET /repos/{owner}/{repo}/issues — list issues
	mux.HandleFunc("GET /repos/testowner/testrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		var result []ghIssue
		for _, issue := range state.issues {
			result = append(result, *issue)
		}
		json.NewEncoder(w).Encode(result)
	})

	// GET /repos/{owner}/{repo}/labels — list labels
	mux.HandleFunc("GET /repos/testowner/testrepo/labels", func(w http.ResponseWriter, r *http.Request) {
		var result []ghLabel
		for name := range state.labels {
			result = append(result, ghLabel{Name: name, Color: "000000"})
		}
		json.NewEncoder(w).Encode(result)
	})

	// POST /repos/{owner}/{repo}/labels — create label
	mux.HandleFunc("POST /repos/testowner/testrepo/labels", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if state.labels[req.Name] {
			http.Error(w, `{"message":"Validation Failed"}`, 422)
			return
		}
		state.labels[req.Name] = true
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(ghLabel{Name: req.Name, Color: req.Color})
	})

	// POST /repos/{owner}/{repo}/issues/{number}/comments — create comment
	mux.HandleFunc("POST /repos/testowner/testrepo/issues/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.Contains(r.URL.Path, "/comments") {
			http.Error(w, "not found", 404)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})

	srv := httptest.NewServer(mux)
	return srv, state
}

type ghServerState struct {
	issues     map[int]*ghIssue
	labels     map[string]bool
	nextNumber int
}

func newTestGitHubBackend(serverURL string) *GitHubBackend {
	b := NewGitHubBackend("testowner", "testrepo", "test-token")
	b.api.baseURL = serverURL
	return b
}

func TestGitHubBackend_Push_CreateIssue(t *testing.T) {
	srv, state := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	payload, _ := json.Marshal(map[string]any{
		"id":          "issue-1",
		"title":       "Fix login bug",
		"description": "The login page crashes",
		"status":      "open",
		"priority":    "p1",
		"type":        "bug",
		"created_at":  time.Now().Format(time.RFC3339),
		"updated_at":  time.Now().Format(time.RFC3339),
	})

	result, err := backend.Push(&PushRequest{
		DeviceID:  "dev1",
		SessionID: "sess1",
		Events: []tdsync.Event{
			{
				ClientActionID:  1,
				ActionType:      "create",
				EntityType:      "issues",
				EntityID:        "issue-1",
				Payload:         payload,
				ClientTimestamp: time.Now(),
			},
		},
	})
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", result.Accepted)
	}
	if len(result.Acks) != 1 {
		t.Fatalf("len(Acks) = %d, want 1", len(result.Acks))
	}
	if result.Acks[0].ServerSeq == 0 {
		t.Error("ServerSeq should be non-zero")
	}

	// Verify issue was created
	if len(state.issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(state.issues))
	}
	ghIssue := state.issues[1]
	if ghIssue.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", ghIssue.Title, "Fix login bug")
	}
	if !strings.Contains(ghIssue.Body, "<!-- td-id:issue-1 -->") {
		t.Error("body should contain td-id marker")
	}

	// Verify labels
	labelNames := make(map[string]bool)
	for _, l := range ghIssue.Labels {
		labelNames[l.Name] = true
	}
	if !labelNames["td:managed"] {
		t.Error("missing td:managed label")
	}
	if !labelNames["td:open"] {
		t.Error("missing td:open label")
	}
	if !labelNames["td:p1"] {
		t.Error("missing td:p1 label")
	}
	if !labelNames["td:bug"] {
		t.Error("missing td:bug label")
	}
}

func TestGitHubBackend_Push_UpdateIssue(t *testing.T) {
	srv, _ := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	// Create first
	createPayload, _ := json.Marshal(map[string]any{
		"id":     "issue-1",
		"title":  "Original title",
		"status": "open",
		"type":   "task",
	})
	_, err := backend.Push(&PushRequest{
		DeviceID: "dev1", SessionID: "sess1",
		Events: []tdsync.Event{{
			ClientActionID: 1, ActionType: "create", EntityType: "issues",
			EntityID: "issue-1", Payload: createPayload, ClientTimestamp: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("Create push: %v", err)
	}

	// Update
	updatePayload, _ := json.Marshal(map[string]any{
		"id":     "issue-1",
		"title":  "Updated title",
		"status": "in_progress",
		"type":   "task",
	})
	result, err := backend.Push(&PushRequest{
		DeviceID: "dev1", SessionID: "sess1",
		Events: []tdsync.Event{{
			ClientActionID: 2, ActionType: "update", EntityType: "issues",
			EntityID: "issue-1", Payload: updatePayload, ClientTimestamp: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("Update push: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", result.Accepted)
	}
}

func TestGitHubBackend_Push_DeleteClosesIssue(t *testing.T) {
	srv, state := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	// Create first
	createPayload, _ := json.Marshal(map[string]any{
		"id": "issue-1", "title": "Test", "status": "open", "type": "task",
	})
	backend.Push(&PushRequest{
		DeviceID: "dev1", SessionID: "sess1",
		Events: []tdsync.Event{{
			ClientActionID: 1, ActionType: "create", EntityType: "issues",
			EntityID: "issue-1", Payload: createPayload, ClientTimestamp: time.Now(),
		}},
	})

	// Delete (close)
	result, err := backend.Push(&PushRequest{
		DeviceID: "dev1", SessionID: "sess1",
		Events: []tdsync.Event{{
			ClientActionID: 2, ActionType: "delete", EntityType: "issues",
			EntityID: "issue-1", Payload: []byte(`{"id":"issue-1"}`), ClientTimestamp: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("Delete push: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", result.Accepted)
	}

	// Verify issue is closed
	if state.issues[1].State != "closed" {
		t.Errorf("State = %q, want %q", state.issues[1].State, "closed")
	}
}

func TestGitHubBackend_Pull(t *testing.T) {
	srv, state := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	// Pre-populate a GitHub Issue with td metadata
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"id": "issue-abc", "title": "Remote task", "status": "open",
		"priority": "p2", "type": "task",
		"created_at": now.Format(time.RFC3339),
		"updated_at": now.Format(time.RFC3339),
	})
	body := buildIssueBody("Remote task description", "issue-abc", payload)
	state.issues[1] = &ghIssue{
		Number:    1,
		Title:     "Remote task",
		Body:      body,
		State:     "open",
		Labels:    []ghLabel{{Name: "td:managed"}, {Name: "td:open"}, {Name: "td:p2"}, {Name: "td:task"}},
		CreatedAt: now,
		UpdatedAt: now,
	}

	result, err := backend.Pull(0, 100, "")
	if err != nil {
		t.Fatalf("Pull() error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1", len(result.Events))
	}

	ev := result.Events[0]
	if ev.EntityID != "issue-abc" {
		t.Errorf("EntityID = %q, want %q", ev.EntityID, "issue-abc")
	}
	if ev.EntityType != "issues" {
		t.Errorf("EntityType = %q, want %q", ev.EntityType, "issues")
	}
	if ev.ActionType != "create" {
		t.Errorf("ActionType = %q, want %q", ev.ActionType, "create")
	}

	// Verify payload has expected fields (payload is wrapped in envelope)
	newData, err := extractNewData(ev.Payload)
	if err != nil {
		t.Fatalf("extractNewData: %v", err)
	}
	var fields map[string]any
	json.Unmarshal(newData, &fields)
	if fields["title"] != "Remote task" {
		t.Errorf("payload title = %v, want %q", fields["title"], "Remote task")
	}
	if fields["status"] != "open" {
		t.Errorf("payload status = %v, want %q", fields["status"], "open")
	}
}

func TestGitHubBackend_Status(t *testing.T) {
	srv, state := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	now := time.Now()
	state.issues[1] = &ghIssue{
		Number: 1, Title: "Task 1", State: "open",
		Body:      "<!-- td-id:t1 -->",
		Labels:    []ghLabel{{Name: "td:managed"}},
		CreatedAt: now, UpdatedAt: now,
	}
	state.issues[2] = &ghIssue{
		Number: 2, Title: "Task 2", State: "open",
		Body:      "<!-- td-id:t2 -->",
		Labels:    []ghLabel{{Name: "td:managed"}},
		CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}

	status, err := backend.Status()
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status.EventCount != 2 {
		t.Errorf("EventCount = %d, want 2", status.EventCount)
	}
}

func TestGitHubBackend_Push_UnsupportedEntityAcked(t *testing.T) {
	srv, _ := fakeGitHubServer(t)
	defer srv.Close()
	backend := newTestGitHubBackend(srv.URL)

	result, err := backend.Push(&PushRequest{
		DeviceID: "dev1", SessionID: "sess1",
		Events: []tdsync.Event{{
			ClientActionID: 1, ActionType: "create", EntityType: "boards",
			EntityID: "board-1", Payload: []byte(`{"id":"board-1","name":"test"}`),
			ClientTimestamp: time.Now(),
		}},
	})
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("unsupported entities should be accepted silently, got Accepted=%d", result.Accepted)
	}
}

// --- Mapper tests ---

func TestParseTdID(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{"<!-- td-id:abc123 -->", "abc123"},
		{"<!-- td-id:abc123 -->\nSome description", "abc123"},
		{"No metadata here", ""},
		{"", ""},
		{"<!-- td-id: spaced-id -->", "spaced-id"},
	}
	for _, tt := range tests {
		got := parseTdID(tt.body)
		if got != tt.want {
			t.Errorf("parseTdID(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestBuildAndParseRoundTrip(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"id": "test-id", "title": "Test Issue", "status": "open",
	})

	body := buildIssueBody("My description", "test-id", payload)

	// Parse back
	gotID := parseTdID(body)
	if gotID != "test-id" {
		t.Errorf("parseTdID = %q, want %q", gotID, "test-id")
	}

	gotPayload := parseStoredPayload(body)
	if gotPayload == nil {
		t.Fatal("parseStoredPayload returned nil")
	}

	var fields map[string]any
	json.Unmarshal(gotPayload, &fields)
	if fields["title"] != "Test Issue" {
		t.Errorf("payload title = %v, want %q", fields["title"], "Test Issue")
	}

	gotDesc := parseDescription(body)
	if gotDesc != "My description" {
		t.Errorf("parseDescription = %q, want %q", gotDesc, "My description")
	}
}

func TestBuildLabels(t *testing.T) {
	labels := buildLabels("in_progress", "p0", "bug")
	expected := map[string]bool{
		"td:managed":     true,
		"td:in-progress": true,
		"td:p0":          true,
		"td:bug":         true,
	}
	got := make(map[string]bool)
	for _, l := range labels {
		got[l] = true
	}
	for name := range expected {
		if !got[name] {
			t.Errorf("missing label %q", name)
		}
	}
}

func TestLabelToStatus(t *testing.T) {
	tests := []struct {
		labels  []ghLabel
		ghState string
		want    string
	}{
		{[]ghLabel{{Name: "td:open"}}, "open", "open"},
		{[]ghLabel{{Name: "td:in-progress"}}, "open", "in_progress"},
		{[]ghLabel{{Name: "td:in-review"}}, "open", "in_review"},
		{[]ghLabel{{Name: "td:blocked"}}, "open", "blocked"},
		{[]ghLabel{}, "closed", "closed"},
		{[]ghLabel{{Name: "td:in-progress"}}, "closed", "closed"}, // closed takes precedence
	}
	for _, tt := range tests {
		got := labelToStatus(tt.labels, tt.ghState)
		if got != tt.want {
			t.Errorf("labelToStatus(%v, %q) = %q, want %q", tt.labels, tt.ghState, got, tt.want)
		}
	}
}

func TestSynthesizePayload(t *testing.T) {
	issue := ghIssue{
		Number:    1,
		Title:     "Test task",
		Body:      "<!-- td-id:synth-1 -->\n\nSome description",
		State:     "open",
		Labels:    []ghLabel{{Name: "td:managed"}, {Name: "td:open"}, {Name: "td:p1"}, {Name: "td:task"}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	payload := synthesizePayload(issue, "synth-1")

	var fields map[string]any
	json.Unmarshal(payload, &fields)

	if fields["id"] != "synth-1" {
		t.Errorf("id = %v, want %q", fields["id"], "synth-1")
	}
	if fields["title"] != "Test task" {
		t.Errorf("title = %v, want %q", fields["title"], "Test task")
	}
	if fields["priority"] != "p1" {
		t.Errorf("priority = %v, want %q", fields["priority"], "p1")
	}
	if fields["type"] != "task" {
		t.Errorf("type = %v, want %q", fields["type"], "task")
	}
}
