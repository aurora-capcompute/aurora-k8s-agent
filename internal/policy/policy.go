package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"aurora-capcompute/aurora"
)

type File struct {
	Version int                   `json:"version"`
	Users   map[string]UserPolicy `json:"users"`
}

type UserPolicy struct {
	AllowedChats []int64         `json:"allowed_chats"`
	Manifest     aurora.Manifest `json:"manifest"`
}

type User struct {
	ID           int64
	AllowedChats map[int64]struct{}
	Manifest     aurora.Manifest
	Digest       string
}

type Set struct {
	users map[int64]User
}

func Load(path string, provider aurora.DispatcherProvider) (*Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	return Parse(raw, provider)
}

func Parse(raw []byte, provider aurora.DispatcherProvider) (*Set, error) {
	var file File
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported policy version %d", file.Version)
	}
	if len(file.Users) == 0 {
		return nil, errors.New("policy must contain at least one user")
	}
	set := &Set{users: make(map[int64]User, len(file.Users))}
	for rawID, configured := range file.Users {
		id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid Telegram user ID %q", rawID)
		}
		manifest, err := aurora.ValidateManifest(configured.Manifest, provider)
		if err != nil {
			return nil, fmt.Errorf("user %d manifest: %w", id, err)
		}
		chats := make(map[int64]struct{}, len(configured.AllowedChats))
		for _, chatID := range configured.AllowedChats {
			if chatID == 0 {
				return nil, fmt.Errorf("user %d has zero chat ID", id)
			}
			chats[chatID] = struct{}{}
		}
		if len(chats) == 0 {
			return nil, fmt.Errorf("user %d must allow at least one chat", id)
		}
		digestInput, _ := json.Marshal(manifest)
		sum := sha256.Sum256(digestInput)
		set.users[id] = User{
			ID: id, AllowedChats: chats, Manifest: manifest,
			Digest: hex.EncodeToString(sum[:]),
		}
	}
	return set, nil
}

func (s *Set) Authorize(userID, chatID int64) (User, bool) {
	user, ok := s.users[userID]
	if !ok {
		return User{}, false
	}
	_, ok = user.AllowedChats[chatID]
	return user, ok
}
