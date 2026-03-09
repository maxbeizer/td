package syncbackend

import (
	"fmt"
	"time"

	tdsync "github.com/marcus/td/internal/sync"
	"github.com/marcus/td/internal/syncclient"
)

// HTTPBackend implements SyncBackend using the existing td-sync HTTP server.
type HTTPBackend struct {
	client    *syncclient.Client
	projectID string
}

// NewHTTPBackend wraps an existing syncclient.Client as a SyncBackend.
func NewHTTPBackend(client *syncclient.Client, projectID string) *HTTPBackend {
	return &HTTPBackend{
		client:    client,
		projectID: projectID,
	}
}

func (h *HTTPBackend) Name() string { return "http" }

func (h *HTTPBackend) Push(req *PushRequest) (*PushResult, error) {
	// Convert to syncclient types
	pushReq := &syncclient.PushRequest{
		DeviceID:  req.DeviceID,
		SessionID: req.SessionID,
	}
	for _, ev := range req.Events {
		pushReq.Events = append(pushReq.Events, syncclient.EventInput{
			ClientActionID:  ev.ClientActionID,
			ActionType:      ev.ActionType,
			EntityType:      ev.EntityType,
			EntityID:        ev.EntityID,
			Payload:         ev.Payload,
			ClientTimestamp: ev.ClientTimestamp.Format(time.RFC3339),
		})
	}

	resp, err := h.client.Push(h.projectID, pushReq)
	if err != nil {
		return nil, fmt.Errorf("http push: %w", err)
	}

	// Convert response back to backend types
	result := &PushResult{Accepted: resp.Accepted}
	for _, a := range resp.Acks {
		result.Acks = append(result.Acks, Ack{
			ClientActionID: a.ClientActionID,
			ServerSeq:      a.ServerSeq,
		})
	}
	for _, r := range resp.Rejected {
		result.Rejected = append(result.Rejected, Rejection{
			ClientActionID: r.ClientActionID,
			Reason:         r.Reason,
			ServerSeq:      r.ServerSeq,
		})
	}
	return result, nil
}

func (h *HTTPBackend) Pull(afterSeq int64, limit int, excludeDeviceID string) (*PullResult, error) {
	resp, err := h.client.Pull(h.projectID, afterSeq, limit, excludeDeviceID)
	if err != nil {
		return nil, fmt.Errorf("http pull: %w", err)
	}

	result := &PullResult{
		LastServerSeq: resp.LastServerSeq,
		HasMore:       resp.HasMore,
	}
	for _, pe := range resp.Events {
		clientTS, _ := time.Parse(time.RFC3339, pe.ClientTimestamp)
		result.Events = append(result.Events, tdsync.Event{
			ServerSeq:       pe.ServerSeq,
			DeviceID:        pe.DeviceID,
			SessionID:       pe.SessionID,
			ClientActionID:  pe.ClientActionID,
			ActionType:      pe.ActionType,
			EntityType:      pe.EntityType,
			EntityID:        pe.EntityID,
			Payload:         pe.Payload,
			ClientTimestamp: clientTS,
		})
	}
	return result, nil
}

func (h *HTTPBackend) Status() (*SyncStatus, error) {
	resp, err := h.client.SyncStatus(h.projectID)
	if err != nil {
		return nil, fmt.Errorf("http status: %w", err)
	}
	return &SyncStatus{
		EventCount:    resp.EventCount,
		LastServerSeq: resp.LastServerSeq,
		LastEventTime: resp.LastEventTime,
	}, nil
}
