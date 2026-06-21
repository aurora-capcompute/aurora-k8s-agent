package state

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

type Update struct {
	ID      int64
	Payload json.RawMessage
}

type Conversation struct {
	UserID       int64
	ChatID       int64
	ThreadID     string
	PolicyDigest string
}

type Elevation struct {
	UserID    int64
	ChatID    int64
	Profile   string
	State     string
	RunID     string
	ExpiresAt time.Time
}

type TaskMessage struct {
	TaskID    string
	RunID     string
	UserID    int64
	ChatID    int64
	MessageID int64
	Token     string
	State     string
}

type RunMessage struct {
	RunID     string
	UserID    int64
	ChatID    int64
	MessageID int64
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
CREATE TABLE IF NOT EXISTS telegram_meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS telegram_updates (
	update_id INTEGER PRIMARY KEY,
	payload BLOB NOT NULL,
	state TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	processed_at TEXT
);
CREATE TABLE IF NOT EXISTS conversations (
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	thread_id TEXT NOT NULL,
	policy_digest TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(user_id, chat_id)
);
CREATE TABLE IF NOT EXISTS elevations (
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	profile TEXT NOT NULL,
	state TEXT NOT NULL,
	run_id TEXT NOT NULL DEFAULT '',
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(user_id, chat_id)
);
CREATE TABLE IF NOT EXISTS elevation_audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	profile TEXT NOT NULL,
	state TEXT NOT NULL,
	run_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_messages (
	task_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	token BLOB NOT NULL,
	state TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_messages (
	run_id TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	state TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS callback_queries (
	callback_id TEXT PRIMARY KEY,
	state TEXT NOT NULL DEFAULT 'processing',
	processed_at TEXT NOT NULL
);`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) Offset(ctx context.Context) (int64, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM telegram_meta WHERE key='update_offset'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var offset int64
	_, err = fmt.Sscan(raw, &offset)
	return offset, err
}

func (s *Store) EnqueueUpdate(ctx context.Context, id int64, payload []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO telegram_updates(update_id,payload,created_at) VALUES(?,?,?)`,
		id, payload, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO telegram_meta(key,value) VALUES('update_offset',?)
ON CONFLICT(key) DO UPDATE SET value =
	CASE WHEN CAST(excluded.value AS INTEGER) > CAST(value AS INTEGER)
	THEN excluded.value ELSE value END`, id+1); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingUpdates(ctx context.Context, limit int) ([]Update, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT update_id,payload FROM telegram_updates
WHERE state='pending' ORDER BY update_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Update
	for rows.Next() {
		var update Update
		if err := rows.Scan(&update.ID, &update.Payload); err != nil {
			return nil, err
		}
		result = append(result, update)
	}
	return result, rows.Err()
}

func (s *Store) CompleteUpdate(ctx context.Context, id int64, processErr error) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if processErr == nil {
		_, err := s.db.ExecContext(ctx,
			`UPDATE telegram_updates SET state='done',processed_at=? WHERE update_id=?`,
			now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE telegram_updates SET attempts=attempts+1,last_error=?,
	state=CASE WHEN attempts>=4 THEN 'failed' ELSE 'pending' END
WHERE update_id=?`, processErr.Error(), id)
	return err
}

func (s *Store) Conversation(ctx context.Context, userID, chatID int64) (Conversation, bool, error) {
	var value Conversation
	err := s.db.QueryRowContext(ctx, `
SELECT user_id,chat_id,thread_id,policy_digest FROM conversations
WHERE user_id=? AND chat_id=?`, userID, chatID).Scan(
		&value.UserID, &value.ChatID, &value.ThreadID, &value.PolicyDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	return value, err == nil, err
}

func (s *Store) SaveConversation(ctx context.Context, value Conversation) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO conversations(user_id,chat_id,thread_id,policy_digest,created_at,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(user_id,chat_id) DO UPDATE SET
	thread_id=excluded.thread_id,policy_digest=excluded.policy_digest,updated_at=excluded.updated_at`,
		value.UserID, value.ChatID, value.ThreadID, value.PolicyDigest, now, now)
	return err
}

func (s *Store) Conversations(ctx context.Context) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id,chat_id,thread_id,policy_digest FROM conversations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Conversation
	for rows.Next() {
		var value Conversation
		if err := rows.Scan(&value.UserID, &value.ChatID, &value.ThreadID, &value.PolicyDigest); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ArmElevation(ctx context.Context, value Elevation) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
INSERT INTO elevations(user_id,chat_id,profile,state,run_id,expires_at,created_at,updated_at)
VALUES(?,?,?,'armed','',?,?,?)
ON CONFLICT(user_id,chat_id) DO UPDATE SET profile=excluded.profile,state='armed',
	run_id='',expires_at=excluded.expires_at,updated_at=excluded.updated_at`,
		value.UserID, value.ChatID, value.Profile, value.ExpiresAt.UTC().Format(time.RFC3339Nano), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
INSERT INTO elevation_audit(user_id,chat_id,profile,state,run_id,created_at)
VALUES(?,?,?,'armed','',?)`, value.UserID, value.ChatID, value.Profile, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Elevation(ctx context.Context, userID, chatID int64) (Elevation, bool, error) {
	var value Elevation
	var expires string
	err := s.db.QueryRowContext(ctx, `
SELECT user_id,chat_id,profile,state,run_id,expires_at FROM elevations
WHERE user_id=? AND chat_id=?`, userID, chatID).Scan(
		&value.UserID, &value.ChatID, &value.Profile, &value.State, &value.RunID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Elevation{}, false, nil
	}
	if err != nil {
		return Elevation{}, false, err
	}
	value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	return value, err == nil, err
}

func (s *Store) BindElevation(ctx context.Context, userID, chatID int64, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profile string
	if err := tx.QueryRowContext(ctx, `
SELECT profile FROM elevations WHERE user_id=? AND chat_id=? AND state IN ('armed','consuming')`,
		userID, chatID).Scan(&profile); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE elevations SET state='consumed',run_id=?,updated_at=?
WHERE user_id=? AND chat_id=? AND state IN ('armed','consuming')`,
		runID, time.Now().UTC().Format(time.RFC3339Nano), userID, chatID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("elevation is not armed")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO elevation_audit(user_id,chat_id,profile,state,run_id,created_at)
VALUES(?,?,?,'consumed',?,?)`, userID, chatID, profile, runID,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginElevation(ctx context.Context, userID, chatID int64) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE elevations SET state='consuming',updated_at=?
WHERE user_id=? AND chat_id=? AND state='armed'`,
		time.Now().UTC().Format(time.RFC3339Nano), userID, chatID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("elevation is not armed")
	}
	return nil
}

func (s *Store) ClearElevation(ctx context.Context, userID, chatID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profile, runID string
	err = tx.QueryRowContext(ctx, `
SELECT profile,run_id FROM elevations WHERE user_id=? AND chat_id=?`,
		userID, chatID).Scan(&profile, &runID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO elevation_audit(user_id,chat_id,profile,state,run_id,created_at)
VALUES(?,?,?,'revoked',?,?)`, userID, chatID, profile, runID,
			time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM elevations WHERE user_id=? AND chat_id=?`, userID, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveTaskMessage(ctx context.Context, task TaskMessage) error {
	token, err := s.encrypt([]byte(task.Token), []byte(task.TaskID))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO task_messages(task_id,run_id,user_id,chat_id,message_id,token,state,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(task_id) DO UPDATE SET message_id=excluded.message_id,token=excluded.token,
	state=excluded.state,updated_at=excluded.updated_at`,
		task.TaskID, task.RunID, task.UserID, task.ChatID, task.MessageID, token, task.State,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) TaskMessage(ctx context.Context, taskID string) (TaskMessage, bool, error) {
	var value TaskMessage
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `
SELECT task_id,run_id,user_id,chat_id,message_id,token,state FROM task_messages WHERE task_id=?`,
		taskID).Scan(&value.TaskID, &value.RunID, &value.UserID, &value.ChatID,
		&value.MessageID, &encrypted, &value.State)
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
SELECT task_id,run_id,user_id,chat_id,message_id,token,state
FROM task_messages WHERE state='pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskMessage
	for rows.Next() {
		var value TaskMessage
		var encrypted []byte
		if err := rows.Scan(&value.TaskID, &value.RunID, &value.UserID, &value.ChatID,
			&value.MessageID, &encrypted, &value.State); err != nil {
			return nil, err
		}
		token, err := s.decrypt(encrypted, []byte(value.TaskID))
		if err != nil {
			return nil, err
		}
		value.Token = string(token)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) SetTaskState(ctx context.Context, taskID, state string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_messages SET state=?,updated_at=? WHERE task_id=?`,
		state, time.Now().UTC().Format(time.RFC3339Nano), taskID)
	return err
}

func (s *Store) SaveRunMessage(ctx context.Context, value RunMessage) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO run_messages(run_id,user_id,chat_id,message_id,state,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(run_id) DO UPDATE SET message_id=excluded.message_id,state=excluded.state,
	updated_at=excluded.updated_at`,
		value.RunID, value.UserID, value.ChatID, value.MessageID, value.State,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RunMessage(ctx context.Context, runID string) (RunMessage, bool, error) {
	var value RunMessage
	err := s.db.QueryRowContext(ctx, `
SELECT run_id,user_id,chat_id,message_id,state FROM run_messages WHERE run_id=?`,
		runID).Scan(&value.RunID, &value.UserID, &value.ChatID, &value.MessageID, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return RunMessage{}, false, nil
	}
	return value, err == nil, err
}

func (s *Store) ClaimCallback(ctx context.Context, id string) (bool, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO callback_queries(callback_id,state,processed_at)
VALUES(?,'processing',?)`, id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	var callbackState string
	if err := s.db.QueryRowContext(ctx,
		`SELECT state FROM callback_queries WHERE callback_id=?`, id).Scan(&callbackState); err != nil {
		return false, err
	}
	return callbackState != "done", nil
}

func (s *Store) CompleteCallback(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE callback_queries SET state='done',processed_at=? WHERE callback_id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
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
