package bot

import (
	"strings"
	"testing"

	"aurora-capcompute/aurora"
	"aurora-k8s-agent/internal/telegram"
)

func TestRenderRunCommunicatesPrivilegeRevocation(t *testing.T) {
	text, keyboard := renderRun(aurora.RunSnapshot{
		ID: "run", Status: aurora.RunCompleted, Answer: "<done>",
	})
	if keyboard != nil {
		t.Fatal("completed run has keyboard")
	}
	if !strings.Contains(text, "&lt;done&gt;") || !strings.Contains(text, "privileges revoked") {
		t.Fatalf("renderRun = %q", text)
	}
}

func TestAcceptedTextRequiresDirectGroupInteraction(t *testing.T) {
	service := &Service{identity: telegram.BotIdentity{ID: 99, Username: "aurora_bot"}}
	group := &telegram.Message{
		Chat: telegram.Chat{ID: -1, Type: "group"},
		Text: "inspect pods",
	}
	if _, ok := service.acceptedText(group); ok {
		t.Fatal("ambient group message was accepted")
	}
	group.Text = "@aurora_bot inspect pods"
	if text, ok := service.acceptedText(group); !ok || text != "inspect pods" {
		t.Fatalf("mentioned text = %q, ok=%v", text, ok)
	}
	group.Text = "what happened?"
	group.ReplyToMessage = &telegram.Message{From: &telegram.User{ID: 99}}
	if _, ok := service.acceptedText(group); !ok {
		t.Fatal("reply to bot was rejected")
	}
}

func TestChunksPreservesContent(t *testing.T) {
	value := strings.Repeat("a", 9000)
	parts := chunks(value, 3900)
	if len(parts) != 3 || strings.Join(parts, "") != value {
		t.Fatalf("chunks produced %d invalid parts", len(parts))
	}
}

func TestRedactedJSONRemovesSensitiveValues(t *testing.T) {
	got := redactedJSON([]byte(`{"username":"ok","password":"bad","nested":{"api_key":"also-bad"}}`))
	if strings.Contains(got, "bad") || !strings.Contains(got, `"username":"ok"`) {
		t.Fatalf("redactedJSON = %s", got)
	}
}
