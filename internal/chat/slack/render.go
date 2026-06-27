package slack

import (
	"encoding/json"
	"strings"
	"time"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/transport/slack"
)

const (
	actionApprove = "task_approve"
	actionDeny    = "task_deny"
	actionCancel  = "run_cancel"
)

func runButtons(runID string) []slack.Button {
	return []slack.Button{{Text: "🛑 Cancel", ActionID: actionCancel, Value: runID, Style: "danger"}}
}

func renderRun(run aurora.RunSnapshot) (string, []slack.Button) {
	switch run.Status {
	case aurora.RunQueued:
		return "⏳ *Queued*", runButtons(run.ID)
	case aurora.RunRunning:
		return "🧠 *Working…*", runButtons(run.ID)
	case aurora.RunWaitingTask:
		return "✋ *Waiting for approval* — use the buttons below.", runButtons(run.ID)
	case aurora.RunInterrupted:
		return "⚠️ *Interrupted* — send a new message or `/aurora new`.", nil
	case aurora.RunCompleted:
		return "✅ *Done*\n\n" + mrkdwn(run.Answer), nil
	case aurora.RunStopped:
		return "🛑 *Cancelled*", nil
	case aurora.RunFailed:
		return "❌ *Failed* — " + mrkdwn(run.Error), nil
	default:
		return "ℹ️ *" + mrkdwn(string(run.Status)) + "*", nil
	}
}

func renderTask(task aurora.TaskSnapshot) string {
	return "⚠️ *Approval required*\n" +
		"*Operation:* `" + mrkdwn(task.Call.Name) + "`\n" +
		"*Request:* `" + mrkdwn(shorten(redactedJSON(task.Call.Args), 800)) + "`\n\n" +
		mrkdwn(task.Summary) + "\n\nExpires " + formatExpiry(task.ExpiresAt) + "."
}

func renderTimerWaiting(fireAt time.Time) string {
	return "⏲ *Timer set* — I'll continue at `" + fireAt.UTC().Format(time.RFC822) + "`."
}

func terminal(status aurora.RunStatus) bool {
	switch status {
	case aurora.RunCompleted, aurora.RunStopped, aurora.RunFailed:
		return true
	default:
		return false
	}
}

func decisionIcon(stateValue aurora.TaskState) string {
	if stateValue == aurora.TaskStateApproved || stateValue == aurora.TaskStateCompleted || stateValue == aurora.TaskStateExecuted {
		return "✅"
	}
	return "⛔"
}

func formatExpiry(value *time.Time) string {
	if value == nil {
		return "later"
	}
	return "`" + value.UTC().Format(time.RFC822) + "`"
}

func helpText() string {
	return strings.Join([]string{
		"*Aurora Slack agent*",
		"",
		"DM me or @mention me with a request and I'll run it through Aurora.",
		"Mutating capabilities show *Approve* / *Deny* buttons before executing.",
		"",
		"`/aurora help` — this message",
		"`/aurora new` — start a fresh conversation",
		"`/aurora status` — current run state",
		"`/aurora cancel` — stop the active run",
	}, "\n")
}

// mrkdwn escapes the three characters Slack treats specially in message text.
func mrkdwn(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

func title(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func shorten(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func redactedJSON(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	redactValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func redactValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") || strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "apikey") {
				typed[key] = "[REDACTED]"
				continue
			}
			redactValue(item)
		}
	case []any:
		for _, item := range typed {
			redactValue(item)
		}
	}
}
