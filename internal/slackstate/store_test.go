package slackstate

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "slack.db"), make([]byte, 32))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestConversationRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	conv := Conversation{UserID: "U1", ChannelID: "C1", ThreadID: "thread-1", PolicyDigest: "abc"}
	if err := store.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, found, err := store.Conversation(ctx, "U1", "C1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.ThreadID != "thread-1" || got.PolicyDigest != "abc" {
		t.Fatalf("got %+v", got)
	}
}

func TestTaskMessageTokenIsEncryptedRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	task := TaskMessage{
		TaskID: "task-1", RunID: "run-1", UserID: "U1", ChannelID: "C1",
		MessageTS: "111.222", Token: "secret-token", State: "pending",
	}
	if err := store.SaveTaskMessage(ctx, task); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, found, err := store.TaskMessage(ctx, "task-1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Token != "secret-token" {
		t.Fatalf("token = %q", got.Token)
	}
	pending, err := store.PendingTaskMessages(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d err=%v", len(pending), err)
	}
}

func TestEventDedup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first, err := store.ClaimEvent(ctx, "evt-1")
	if err != nil || !first {
		t.Fatalf("first claim should succeed: %v %v", first, err)
	}
	if err := store.CompleteEvent(ctx, "evt-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	again, err := store.ClaimEvent(ctx, "evt-1")
	if err != nil {
		t.Fatalf("second claim err: %v", err)
	}
	if again {
		t.Fatal("a completed event must not be claimable again")
	}
}
