// Package syncbackend defines the SyncBackend interface for pluggable sync transports.
//
// The default implementation wraps the existing td-sync HTTP server client.
// Alternative implementations (e.g., GitHub Issues) can be added by implementing
// this interface.
package syncbackend

import (
	tdsync "github.com/marcus/td/internal/sync"
)

// SyncBackend abstracts the remote sync transport.
// The local side (GetPendingEvents, ApplyEvents, action_log) remains unchanged;
// only the push/pull transport is swapped.
type SyncBackend interface {
	// Push sends local events to the remote backend.
	// Returns which events were accepted, acknowledged, or rejected.
	Push(req *PushRequest) (*PushResult, error)

	// Pull fetches remote events that occurred after the given sequence number.
	Pull(afterSeq int64, limit int, excludeDeviceID string) (*PullResult, error)

	// Status returns the current sync status from the remote backend.
	Status() (*SyncStatus, error)

	// Name returns a human-readable name for this backend (e.g., "http", "github").
	Name() string
}

// PushRequest contains the events to push to the remote backend.
type PushRequest struct {
	DeviceID  string
	SessionID string
	Events    []tdsync.Event
}

// PushResult is the response from a push operation.
type PushResult struct {
	Accepted int
	Acks     []Ack
	Rejected []Rejection
}

// Ack confirms a client action was accepted with a server sequence number.
type Ack struct {
	ClientActionID int64
	ServerSeq      int64
}

// Rejection explains why a client action was refused.
type Rejection struct {
	ClientActionID int64
	Reason         string
	ServerSeq      int64 // populated for "duplicate" rejections
}

// PullResult is the response from a pull operation.
type PullResult struct {
	Events        []tdsync.Event
	LastServerSeq int64
	HasMore       bool
}

// SyncStatus represents the remote backend's current state.
type SyncStatus struct {
	EventCount    int64
	LastServerSeq int64
	LastEventTime string
}
