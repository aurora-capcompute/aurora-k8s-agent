package telegram

import (
	"encoding/json"
	"html"
	"strings"
	"time"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"
)

func renderRun(run aurora.RunSnapshot) (string, *telegram.InlineKeyboardMarkup) {
	switch run.Status {
	case aurora.RunQueued:
		return "⏳ <b>Queued</b>", stopKeyboard(run.ID)
	case aurora.RunRunning:
		return "🧠 <b>Thinking and inspecting the cluster…</b>", stopKeyboard(run.ID)
	case aurora.RunWaitingTask:
		return "✋ <b>Waiting for your approval</b>\nReview the approval card below.", stopKeyboard(run.ID)
	case aurora.RunInterrupted:
		return "⚠️ <b>Session interrupted</b>\nUse /retry to reconstruct and resume it.", nil
	case aurora.RunCompleted:
		return "✅ <b>Completed</b>\n\n" + escape(run.Answer), nil
	case aurora.RunStopped:
		return "🛑 <b>Cancelled</b>", nil
	case aurora.RunFailed:
		return "❌ <b>Failed</b>\n" + escape(run.Error) + "\n\nUse /retry or /new.", nil
	default:
		return "ℹ️ <b>" + escape(string(run.Status)) + "</b>", nil
	}
}

func renderTask(task aurora.TaskSnapshot) string {
	return "⚠️ <b>Approval required</b>\n\n" +
		"<b>Operation:</b> <code>" + escape(task.Call.Name) + "</code>\n" +
		"<b>Request:</b> <code>" + escape(shorten(redactedJSON(task.Call.Args), 1200)) + "</code>\n\n" +
		escape(task.Summary) + "\n\nTask expires " + formatExpiry(task.ExpiresAt) + "."
}

func renderTimerWaiting(fireAt time.Time) string {
	return "⏲ <b>Timer set</b>\nI'll continue at <code>" +
		escape(fireAt.Local().Format(time.RFC822)) + "</code>."
}

func stopKeyboard(runID string) *telegram.InlineKeyboardMarkup {
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
		{Text: "🛑 Cancel session", CallbackData: "stop:" + runID},
	}}}
}

func terminal(status aurora.RunStatus) bool {
	switch status {
	case aurora.RunCompleted, aurora.RunStopped, aurora.RunFailed:
		return true
	default:
		return false
	}
}

func decisionIcon(state aurora.TaskState) string {
	if state == aurora.TaskStateApproved || state == aurora.TaskStateCompleted || state == aurora.TaskStateExecuted {
		return "✅"
	}
	return "⛔"
}

func formatExpiry(value *time.Time) string {
	if value == nil {
		return "later"
	}
	return "<code>" + escape(value.Local().Format(time.RFC822)) + "</code>"
}

func helpText() string {
	return `🤖 <b>Aurora Kubernetes agent</b>

Send a request to inspect or manage Kubernetes resources.

/status — current session
/history — recent conversation history
/journal — dispatcher calls from the latest run
/cancel — stop the active session
/retry — reconstruct and retry the latest interrupted session
/new — start a fresh conversation

Mutating operations (apply, delete) show an approval card before executing.`
}

func escape(value string) string { return html.EscapeString(value) }

func shorten(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func chunks(value string, limit int) []string {
	if len(value) <= limit {
		return []string{value}
	}
	var result []string
	for len(value) > limit {
		cut := strings.LastIndex(value[:limit], "\n")
		if cut < limit/2 {
			cut = limit
		}
		result = append(result, value[:cut])
		value = strings.TrimLeft(value[cut:], "\n")
	}
	if value != "" {
		result = append(result, value)
	}
	return result
}

func title(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
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
			if strings.Contains(lower, "password") ||
				strings.Contains(lower, "token") ||
				strings.Contains(lower, "secret") ||
				strings.Contains(lower, "api_key") ||
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

func decodeEvent(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
