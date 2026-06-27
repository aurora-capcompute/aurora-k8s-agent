package slack

import (
	"strings"
	"testing"

	"aurora-capcompute/aurora"
)

func TestCleanMentions(t *testing.T) {
	cases := map[string]string{
		"<@U0BOT> list pods": "list pods",
		"hello <@U0BOT>":     "hello",
		"   <@U0BOT>   ":     "",
		"no mention here":    "no mention here",
	}
	for in, want := range cases {
		if got := cleanMentions(in); got != want {
			t.Fatalf("cleanMentions(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMrkdwnEscapes(t *testing.T) {
	if got := mrkdwn("a < b & c > d"); got != "a &lt; b &amp; c &gt; d" {
		t.Fatalf("mrkdwn = %q", got)
	}
}

func TestRenderRunButtons(t *testing.T) {
	text, buttons := renderRun(aurora.RunSnapshot{ID: "run-1", Status: aurora.RunRunning})
	if !strings.Contains(text, "Working") {
		t.Fatalf("running text = %q", text)
	}
	if len(buttons) != 1 || buttons[0].ActionID != actionCancel || buttons[0].Value != "run-1" {
		t.Fatalf("buttons = %+v", buttons)
	}
	_, done := renderRun(aurora.RunSnapshot{ID: "run-1", Status: aurora.RunCompleted, Answer: "ok"})
	if done != nil {
		t.Fatalf("completed run should have no buttons, got %+v", done)
	}
}

func TestRedactedJSONHidesSecrets(t *testing.T) {
	got := redactedJSON([]byte(`{"token":"abc","name":"pod"}`))
	if strings.Contains(got, "abc") || !strings.Contains(got, "REDACTED") {
		t.Fatalf("redactedJSON = %q", got)
	}
}
