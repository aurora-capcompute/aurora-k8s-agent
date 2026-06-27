package state

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsInboxConversationAndEncryptedTask(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	store, err := Open(path, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.EnqueueUpdate(ctx, 7, []byte(`{"update_id":7}`)); err != nil {
		t.Fatalf("EnqueueUpdate: %v", err)
	}
	if offset, _ := store.Offset(ctx); offset != 8 {
		t.Fatalf("offset = %d", offset)
	}
	pending, err := store.PendingUpdates(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingUpdates = %v, %v", pending, err)
	}
	if err := store.CompleteUpdate(ctx, 7, nil); err != nil {
		t.Fatal(err)
	}
	conversation := Conversation{UserID: 1, ChatID: 2, ThreadID: "thread", PolicyDigest: "digest"}
	if err := store.SaveConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := store.Conversation(ctx, 1, 2); !found || got.ThreadID != "thread" {
		t.Fatalf("conversation = %+v, found=%v", got, found)
	}
	if err := store.SaveTaskMessage(ctx, TaskMessage{
		TaskID: "task", RunID: "run", UserID: 1, ChatID: 2,
		MessageID: 3, Token: "super-secret", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("super-secret")) {
		t.Fatal("task token was stored in plaintext")
	}
	reopened, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	task, found, err := reopened.TaskMessage(ctx, "task")
	if err != nil || !found || task.Token != "super-secret" {
		t.Fatalf("task = %+v, found=%v, err=%v", task, found, err)
	}
}

func TestClaimCallbackIsIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.ClaimCallback(context.Background(), "callback")
	if err != nil || !first {
		t.Fatalf("first = %v, err=%v", first, err)
	}
	second, err := store.ClaimCallback(context.Background(), "callback")
	if err != nil || !second {
		t.Fatalf("retry before completion = %v, err=%v", second, err)
	}
	if err := store.CompleteCallback(context.Background(), "callback"); err != nil {
		t.Fatal(err)
	}
	third, err := store.ClaimCallback(context.Background(), "callback")
	if err != nil || third {
		t.Fatalf("third = %v, err=%v", third, err)
	}
}
