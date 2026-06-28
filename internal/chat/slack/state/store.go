// Package state is the Slack adapter's durable bridge state: an encrypted SQLite
// store of the conversation-to-thread mapping and event-dedup claims, keyed by
// Slack user and channel IDs. It owns persistence of what the adapter must
// survive a restart; run and approval state live in the Aurora runtime.
package state

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is the encrypted SQLite bridge state. Slack message references are a
// (channel, ts) pair; conversations and identities are keyed by Slack string IDs.
type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

type Conversation struct {
	UserID       string
	ChannelID    string
	ThreadID     string
	PolicyDigest string
}

type RunMessage struct {
	RunID     string
	UserID    string
	ChannelID string
	MessageTS string
	State     string
}

type TaskMessage struct {
	TaskID    string
	RunID     string
	UserID    string
	ChannelID string
	MessageTS string
	Token     string
	State     string
}

func Open(path string, encryptionKey []byte) (*Store, error) {
	if len(encryptionKey) != 32 {
		return nil, errors.New("state encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, aead: aead}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS conversations (
	user_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	policy_digest TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(user_id, channel_id)
);
CREATE TABLE IF NOT EXISTS run_messages (
	run_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	message_ts TEXT NOT NULL,
	state TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_messages (
	task_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	message_ts TEXT NOT NULL,
	token BLOB NOT NULL,
	state TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS processed_events (
	event_id TEXT PRIMARY KEY,
	state TEXT NOT NULL DEFAULT 'processing',
	processed_at TEXT NOT NULL
);`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }


func (s *Store) Conversation(ctx context.Context, userID, channelID string) (Conversation, bool, error) {
	var value Conversation
	err := s.db.QueryRowContext(ctx, `
SELECT user_id,channel_id,thread_id,policy_digest FROM conversations
WHERE user_id=? AND channel_id=?`, userID, channelID).Scan(
		&value.UserID, &value.ChannelID, &value.ThreadID, &value.PolicyDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	return value, err == nil, err
}

func (s *Store) SaveConversation(ctx context.Context, value Conversation) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO conversations(user_id,channel_id,thread_id,policy_digest,created_at,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(user_id,channel_id) DO UPDATE SET
	thread_id=excluded.thread_id,policy_digest=excluded.policy_digest,updated_at=excluded.updated_at`,
		value.UserID, value.ChannelID, value.ThreadID, value.PolicyDigest, now(), now())
	return err
}

func (s *Store) Conversations(ctx context.Context) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id,channel_id,thread_id,policy_digest FROM conversations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Conversation
	for rows.Next() {
		var value Conversation
		if err := rows.Scan(&value.UserID, &value.ChannelID, &value.ThreadID, &value.PolicyDigest); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) SaveRunMessage(ctx context.Context, value RunMessage) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO run_messages(run_id,user_id,channel_id,message_ts,state,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(run_id) DO UPDATE SET message_ts=excluded.message_ts,state=excluded.state,
	updated_at=excluded.updated_at`,
		value.RunID, value.UserID, value.ChannelID, value.MessageTS, value.State, now())
	return err
}

func (s *Store) RunMessage(ctx context.Context, runID string) (RunMessage, bool, error) {
	var value RunMessage
	err := s.db.QueryRowContext(ctx, `
SELECT run_id,user_id,channel_id,message_ts,state FROM run_messages WHERE run_id=?`,
		runID).Scan(&value.RunID, &value.UserID, &value.ChannelID, &value.MessageTS, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return RunMessage{}, false, nil
	}
	return value, err == nil, err
}

func (s *Store) SaveTaskMessage(ctx context.Context, task TaskMessage) error {
	token, err := s.encrypt([]byte(task.Token), []byte(task.TaskID))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO task_messages(task_id,run_id,user_id,channel_id,message_ts,token,state,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(task_id) DO UPDATE SET message_ts=excluded.message_ts,token=excluded.token,
	state=excluded.state,updated_at=excluded.updated_at`,
		task.TaskID, task.RunID, task.UserID, task.ChannelID, task.MessageTS, token, task.State, now())
	return err
}

func (s *Store) TaskMessage(ctx context.Context, taskID string) (TaskMessage, bool, error) {
	value, encrypted, err := s.scanTask(s.db.QueryRowContext(ctx, `
SELECT task_id,run_id,user_id,channel_id,message_ts,token,state FROM task_messages WHERE task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return TaskMessage{}, false, nil
	}
	if err != nil {
		return TaskMessage{}, false, err
	}
	token, err := s.decrypt(encrypted, []byte(taskID))
	if err != nil {
		return TaskMessage{}, false, err
	}
	value.Token = string(token)
	return value, true, nil
}

func (s *Store) PendingTaskMessages(ctx context.Context) ([]TaskMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT task_id,run_id,user_id,channel_id,message_ts,token,state
FROM task_messages WHERE state='pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskMessage
	for rows.Next() {
		value, encrypted, scanErr := s.scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		token, decErr := s.decrypt(encrypted, []byte(value.TaskID))
		if decErr != nil {
			return nil, decErr
		}
		value.Token = string(token)
		result = append(result, value)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanTask(row scanner) (TaskMessage, []byte, error) {
	var value TaskMessage
	var encrypted []byte
	err := row.Scan(&value.TaskID, &value.RunID, &value.UserID, &value.ChannelID,
		&value.MessageTS, &encrypted, &value.State)
	return value, encrypted, err
}

func (s *Store) SetTaskState(ctx context.Context, taskID, state string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_messages SET state=?,updated_at=? WHERE task_id=?`, state, now(), taskID)
	return err
}

// ClaimEvent records a Slack envelope/event ID for idempotent processing. It
// returns true when the caller should process the event (first observation) and
// false when it has already been completed.
func (s *Store) ClaimEvent(ctx context.Context, id string) (bool, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO processed_events(event_id,state,processed_at) VALUES(?,'processing',?)`,
		id, now()); err != nil {
		return false, err
	}
	var eventState string
	if err := s.db.QueryRowContext(ctx,
		`SELECT state FROM processed_events WHERE event_id=?`, id).Scan(&eventState); err != nil {
		return false, err
	}
	return eventState != "done", nil
}

func (s *Store) CompleteEvent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE processed_events SET state='done',processed_at=? WHERE event_id=?`, now(), id)
	return err
}

func (s *Store) encrypt(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, aad), nil
}

func (s *Store) decrypt(ciphertext, aad []byte) ([]byte, error) {
	size := s.aead.NonceSize()
	if len(ciphertext) < size {
		return nil, errors.New("encrypted value is truncated")
	}
	return s.aead.Open(nil, ciphertext[:size], ciphertext[size:], aad)
}
