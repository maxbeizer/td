package syncbackend

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	tdIDMarkerPrefix = "<!-- td-id:"
	tdIDMarkerSuffix = " -->"
	payloadStart     = "<!-- td-payload-start -->"
	payloadEnd       = "<!-- td-payload-end -->"
)

// extractNewData unwraps the sync event envelope to get the actual entity data.
// Events have format: {"schema_version":1,"new_data":{...},"previous_data":{...}}
func extractNewData(payload json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		NewData json.RawMessage `json:"new_data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if envelope.NewData == nil {
		// If there's no envelope, the payload might be raw data (e.g., in tests)
		return payload, nil
	}
	return envelope.NewData, nil
}

// wrapPayload wraps raw entity data in the envelope format expected by ApplyRemoteEvents.
func wrapPayload(newData json.RawMessage) json.RawMessage {
	envelope := map[string]any{
		"schema_version": 1,
		"new_data":       json.RawMessage(newData),
		"previous_data":  nil,
	}
	data, _ := json.Marshal(envelope)
	return data
}

// buildIssueBody constructs a GitHub Issue body with embedded td metadata.
// Format:
//
//	<!-- td-id:abc123 -->
//	Description text...
//
//	<details><summary>🔧 td sync metadata</summary>
//	<!-- td-payload-start -->
//	```json
//	{full payload}
//	```
//	<!-- td-payload-end -->
//	</details>
func buildIssueBody(description, tdID string, payload json.RawMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s\n", tdIDMarkerPrefix, tdID, tdIDMarkerSuffix)

	if description != "" {
		b.WriteString("\n")
		b.WriteString(description)
		b.WriteString("\n")
	}

	b.WriteString("\n<details><summary>🔧 td sync metadata</summary>\n\n")
	b.WriteString(payloadStart + "\n")
	b.WriteString("```json\n")

	// Pretty-print the payload for readability
	var pretty json.RawMessage
	if err := json.Unmarshal(payload, &pretty); err == nil {
		if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			b.Write(formatted)
		} else {
			b.Write(payload)
		}
	} else {
		b.Write(payload)
	}

	b.WriteString("\n```\n")
	b.WriteString(payloadEnd + "\n")
	b.WriteString("\n</details>\n")

	return b.String()
}

// parseTdID extracts the td issue ID from a GitHub Issue body.
func parseTdID(body string) string {
	idx := strings.Index(body, tdIDMarkerPrefix)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(tdIDMarkerPrefix):]
	end := strings.Index(rest, tdIDMarkerSuffix)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// parseStoredPayload extracts the JSON payload stored in the issue body metadata.
func parseStoredPayload(body string) json.RawMessage {
	startIdx := strings.Index(body, payloadStart)
	if startIdx < 0 {
		return nil
	}
	after := body[startIdx+len(payloadStart):]

	endIdx := strings.Index(after, payloadEnd)
	if endIdx < 0 {
		return nil
	}
	block := after[:endIdx]

	// Extract JSON from the code fence
	jsonStart := strings.Index(block, "```json\n")
	if jsonStart < 0 {
		return nil
	}
	jsonContent := block[jsonStart+len("```json\n"):]

	jsonEnd := strings.Index(jsonContent, "\n```")
	if jsonEnd < 0 {
		return nil
	}

	raw := strings.TrimSpace(jsonContent[:jsonEnd])
	if !json.Valid([]byte(raw)) {
		return nil
	}
	return json.RawMessage(raw)
}

// parseDescription extracts the human-readable description from the body,
// excluding the td_id comment and metadata section.
func parseDescription(body string) string {
	// Remove the td-id line
	desc := body
	if idx := strings.Index(desc, tdIDMarkerPrefix); idx >= 0 {
		end := strings.Index(desc[idx:], "\n")
		if end >= 0 {
			desc = desc[:idx] + desc[idx+end+1:]
		}
	}
	// Remove the details block
	if idx := strings.Index(desc, "<details>"); idx >= 0 {
		desc = desc[:idx]
	}
	return strings.TrimSpace(desc)
}

// buildLabels constructs the label set for a GitHub Issue from td fields.
func buildLabels(status, priority, issueType string) []string {
	labels := []string{labelTdManaged}

	if sl := statusToLabel(status); sl != "" {
		labels = append(labels, sl)
	}
	if pl := priorityToLabel(priority); pl != "" {
		labels = append(labels, pl)
	}
	if tl := typeToLabel(issueType); tl != "" {
		labels = append(labels, tl)
	}

	return labels
}

// statusToLabel maps a td status to a GitHub label name.
func statusToLabel(status string) string {
	switch strings.ToLower(status) {
	case "open":
		return "td:open"
	case "in_progress":
		return "td:in-progress"
	case "in_review":
		return "td:in-review"
	case "blocked":
		return "td:blocked"
	case "closed":
		return "" // closing is handled by GitHub state, not a label
	default:
		return ""
	}
}

// labelToStatus maps GitHub labels back to a td status.
func labelToStatus(labels []ghLabel, ghState string) string {
	if ghState == "closed" {
		return "closed"
	}
	for _, l := range labels {
		switch l.Name {
		case "td:in-progress":
			return "in_progress"
		case "td:in-review":
			return "in_review"
		case "td:blocked":
			return "blocked"
		case "td:open":
			return "open"
		}
	}
	return "open"
}

// priorityToLabel maps a td priority to a GitHub label name.
func priorityToLabel(priority string) string {
	switch strings.ToLower(priority) {
	case "p0":
		return "td:p0"
	case "p1":
		return "td:p1"
	case "p2":
		return "td:p2"
	case "p3":
		return "td:p3"
	default:
		return ""
	}
}

// labelToPriority maps GitHub labels back to a td priority.
func labelToPriority(labels []ghLabel) string {
	for _, l := range labels {
		switch l.Name {
		case "td:p0":
			return "p0"
		case "td:p1":
			return "p1"
		case "td:p2":
			return "p2"
		case "td:p3":
			return "p3"
		}
	}
	return ""
}

// typeToLabel maps a td issue type to a GitHub label name.
func typeToLabel(issueType string) string {
	switch strings.ToLower(issueType) {
	case "task":
		return "td:task"
	case "bug":
		return "td:bug"
	case "feature":
		return "td:feature"
	case "epic":
		return "td:epic"
	default:
		return ""
	}
}

// labelToType maps GitHub labels back to a td issue type.
func labelToType(labels []ghLabel) string {
	for _, l := range labels {
		switch l.Name {
		case "td:task":
			return "task"
		case "td:bug":
			return "bug"
		case "td:feature":
			return "feature"
		case "td:epic":
			return "epic"
		}
	}
	return ""
}

// synthesizePayload builds a td issue payload from GitHub Issue fields
// when no stored payload is available (e.g., issue created on GitHub directly).
func synthesizePayload(issue ghIssue, tdID string) json.RawMessage {
	desc := parseDescription(issue.Body)
	status := labelToStatus(issue.Labels, issue.State)
	priority := labelToPriority(issue.Labels)
	issueType := labelToType(issue.Labels)

	if priority == "" {
		priority = "p2"
	}
	if issueType == "" {
		issueType = "task"
	}

	fields := map[string]any{
		"id":          tdID,
		"title":       issue.Title,
		"description": desc,
		"status":      status,
		"priority":    priority,
		"type":        issueType,
		"created_at":  issue.CreatedAt.Format(time.RFC3339),
		"updated_at":  issue.UpdatedAt.Format(time.RFC3339),
	}

	if issue.ClosedAt != nil {
		fields["closed_at"] = issue.ClosedAt.Format(time.RFC3339)
	}

	data, _ := json.Marshal(fields)
	return data
}

// mergeGitHubState updates a stored payload with the current GitHub state
// (labels might have changed on GitHub since the payload was stored).
func mergeGitHubState(storedPayload json.RawMessage, issue ghIssue) json.RawMessage {
	var fields map[string]any
	if err := json.Unmarshal(storedPayload, &fields); err != nil {
		return storedPayload
	}

	// Update fields that can change on GitHub
	fields["title"] = issue.Title
	fields["status"] = labelToStatus(issue.Labels, issue.State)
	fields["updated_at"] = issue.UpdatedAt.Format(time.RFC3339)

	if p := labelToPriority(issue.Labels); p != "" {
		fields["priority"] = p
	}
	if t := labelToType(issue.Labels); t != "" {
		fields["type"] = t
	}

	desc := parseDescription(issue.Body)
	if desc != "" {
		fields["description"] = desc
	}

	if issue.ClosedAt != nil {
		fields["closed_at"] = issue.ClosedAt.Format(time.RFC3339)
	}

	data, _ := json.Marshal(fields)
	return data
}

// formatCommentBody formats a log/comment/handoff event as a GitHub comment.
func formatCommentBody(entityType string, fields map[string]any) string {
	var b strings.Builder

	switch entityType {
	case "logs":
		msg, _ := fields["message"].(string)
		logType, _ := fields["type"].(string)
		if logType != "" {
			fmt.Fprintf(&b, "**[%s]** ", logType)
		}
		b.WriteString(msg)

	case "handoffs":
		b.WriteString("## 🤝 Handoff\n\n")
		if done, ok := fields["done"].([]any); ok && len(done) > 0 {
			b.WriteString("**Done:**\n")
			for _, d := range done {
				fmt.Fprintf(&b, "- %v\n", d)
			}
		}
		if remaining, ok := fields["remaining"].([]any); ok && len(remaining) > 0 {
			b.WriteString("\n**Remaining:**\n")
			for _, r := range remaining {
				fmt.Fprintf(&b, "- %v\n", r)
			}
		}
		if decisions, ok := fields["decisions"].([]any); ok && len(decisions) > 0 {
			b.WriteString("\n**Decisions:**\n")
			for _, d := range decisions {
				fmt.Fprintf(&b, "- %v\n", d)
			}
		}

	case "comments":
		msg, _ := fields["message"].(string)
		if msg == "" {
			msg, _ = fields["content"].(string)
		}
		b.WriteString(msg)

	default:
		data, _ := json.MarshalIndent(fields, "", "  ")
		b.Write(data)
	}

	// Add hidden metadata for sync tracking
	if id, ok := fields["id"].(string); ok {
		fmt.Fprintf(&b, "\n\n<!-- td-entity:%s:%s -->", entityType, id)
	}

	return b.String()
}
