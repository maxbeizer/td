package syncbackend

import (
	"fmt"

	"github.com/marcus/td/internal/syncclient"
	"github.com/marcus/td/internal/syncconfig"
)

// NewBackend creates a SyncBackend based on the current configuration.
// For now, only "http" is fully implemented. "github" returns an error
// until the GitHub backend is built.
func NewBackend(projectID string) (SyncBackend, error) {
	backendType := syncconfig.GetSyncBackend()

	switch backendType {
	case "http", "":
		deviceID, err := syncconfig.GetDeviceID()
		if err != nil {
			return nil, fmt.Errorf("get device id: %w", err)
		}
		serverURL := syncconfig.GetServerURL()
		apiKey := syncconfig.GetAPIKey()
		client := syncclient.New(serverURL, apiKey, deviceID)
		return NewHTTPBackend(client, projectID), nil

	case "github":
		return nil, fmt.Errorf("github sync backend not yet implemented (coming soon)")

	default:
		return nil, fmt.Errorf("unknown sync backend %q (supported: http, github)", backendType)
	}
}
