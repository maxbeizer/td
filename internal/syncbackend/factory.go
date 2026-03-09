package syncbackend

import (
	"fmt"
	"strings"

	"github.com/marcus/td/internal/syncclient"
	"github.com/marcus/td/internal/syncconfig"
)

// NewBackend creates a SyncBackend based on the current configuration.
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
		repo := syncconfig.GetGitHubRepo()
		if repo == "" {
			return nil, fmt.Errorf("github backend requires sync.github.repo config\n  Run: td config set sync.github.repo owner/repo")
		}
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid github repo format %q (expected owner/repo)", repo)
		}
		token, err := getGitHubToken()
		if err != nil {
			return nil, fmt.Errorf("github auth: %w", err)
		}
		return NewGitHubBackend(parts[0], parts[1], token), nil

	default:
		return nil, fmt.Errorf("unknown sync backend %q (supported: http, github)", backendType)
	}
}
