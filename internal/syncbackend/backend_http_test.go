package syncbackend_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tdsync "github.com/marcus/td/internal/sync"
	"github.com/marcus/td/internal/syncbackend"
	"github.com/marcus/td/internal/syncclient"
)

func TestHTTPBackend_ImplementsInterface(t *testing.T) {
	// Compile-time check that HTTPBackend implements SyncBackend
	var _ syncbackend.SyncBackend = (*syncbackend.HTTPBackend)(nil)
}

func TestHTTPBackend_Name(t *testing.T) {
	b := syncbackend.NewHTTPBackend(syncclient.New("http://localhost", "", "dev1"), "proj1")
	if b.Name() != "http" {
		t.Errorf("Name() = %q, want %q", b.Name(), "http")
	}
}

func TestHTTPBackend_Push(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/projects/proj1/sync/push" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		resp := syncclient.PushResponse{
			Accepted: 2,
			Acks: []syncclient.AckResponse{
				{ClientActionID: 1, ServerSeq: 100},
				{ClientActionID: 2, ServerSeq: 101},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := syncclient.New(server.URL, "key", "dev1")
	backend := syncbackend.NewHTTPBackend(client, "proj1")

	result, err := backend.Push(&syncbackend.PushRequest{
		DeviceID:  "dev1",
		SessionID: "sess1",
		Events: []tdsync.Event{
			{ClientActionID: 1, ActionType: "create", EntityType: "issues", EntityID: "i1", Payload: []byte(`{}`), ClientTimestamp: time.Now()},
			{ClientActionID: 2, ActionType: "update", EntityType: "issues", EntityID: "i2", Payload: []byte(`{}`), ClientTimestamp: time.Now()},
		},
	})
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}
	if result.Accepted != 2 {
		t.Errorf("Accepted = %d, want 2", result.Accepted)
	}
	if len(result.Acks) != 2 {
		t.Errorf("len(Acks) = %d, want 2", len(result.Acks))
	}
	if result.Acks[0].ServerSeq != 100 {
		t.Errorf("Acks[0].ServerSeq = %d, want 100", result.Acks[0].ServerSeq)
	}
}

func TestHTTPBackend_Pull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		resp := syncclient.PullResponse{
			Events: []syncclient.PullEvent{
				{
					ServerSeq:       50,
					DeviceID:        "other-dev",
					SessionID:       "sess2",
					ClientActionID:  10,
					ActionType:      "create",
					EntityType:      "issues",
					EntityID:        "i3",
					Payload:         json.RawMessage(`{"title":"test"}`),
					ClientTimestamp: time.Now().Format(time.RFC3339),
				},
			},
			LastServerSeq: 50,
			HasMore:       false,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := syncclient.New(server.URL, "key", "dev1")
	backend := syncbackend.NewHTTPBackend(client, "proj1")

	result, err := backend.Pull(0, 1000, "dev1")
	if err != nil {
		t.Fatalf("Pull() error: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1", len(result.Events))
	}
	if result.Events[0].EntityID != "i3" {
		t.Errorf("Events[0].EntityID = %q, want %q", result.Events[0].EntityID, "i3")
	}
	if result.LastServerSeq != 50 {
		t.Errorf("LastServerSeq = %d, want 50", result.LastServerSeq)
	}
	if result.HasMore {
		t.Error("HasMore = true, want false")
	}
}

func TestHTTPBackend_Status(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := syncclient.SyncStatusResponse{
			EventCount:    42,
			LastServerSeq: 42,
			LastEventTime: "2026-03-09T20:00:00Z",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := syncclient.New(server.URL, "key", "dev1")
	backend := syncbackend.NewHTTPBackend(client, "proj1")

	status, err := backend.Status()
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status.EventCount != 42 {
		t.Errorf("EventCount = %d, want 42", status.EventCount)
	}
	if status.LastServerSeq != 42 {
		t.Errorf("LastServerSeq = %d, want 42", status.LastServerSeq)
	}
}

func TestHTTPBackend_PushWithRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := syncclient.PushResponse{
			Accepted: 1,
			Acks: []syncclient.AckResponse{
				{ClientActionID: 1, ServerSeq: 100},
			},
			Rejected: []syncclient.RejectResponse{
				{ClientActionID: 2, Reason: "duplicate", ServerSeq: 50},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := syncclient.New(server.URL, "key", "dev1")
	backend := syncbackend.NewHTTPBackend(client, "proj1")

	result, err := backend.Push(&syncbackend.PushRequest{
		DeviceID:  "dev1",
		SessionID: "sess1",
		Events: []tdsync.Event{
			{ClientActionID: 1, ActionType: "create", EntityType: "issues", EntityID: "i1", Payload: []byte(`{}`), ClientTimestamp: time.Now()},
			{ClientActionID: 2, ActionType: "create", EntityType: "issues", EntityID: "i2", Payload: []byte(`{}`), ClientTimestamp: time.Now()},
		},
	})
	if err != nil {
		t.Fatalf("Push() error: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("Accepted = %d, want 1", result.Accepted)
	}
	if len(result.Rejected) != 1 {
		t.Fatalf("len(Rejected) = %d, want 1", len(result.Rejected))
	}
	if result.Rejected[0].Reason != "duplicate" {
		t.Errorf("Rejected[0].Reason = %q, want %q", result.Rejected[0].Reason, "duplicate")
	}
	if result.Rejected[0].ServerSeq != 50 {
		t.Errorf("Rejected[0].ServerSeq = %d, want 50", result.Rejected[0].ServerSeq)
	}
}
