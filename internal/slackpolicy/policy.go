package slackpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"aurora-capcompute/aurora"
)

type File struct {
	Version int                   `json:"version"`
	Users   map[string]UserPolicy `json:"users"`
}

type UserPolicy struct {
	// AllowedChannels lists the Slack channel IDs (C…, G…, or D… for DMs) in
	// which this user may drive the agent.
	AllowedChannels []string        `json:"allowed_channels"`
	Manifest        aurora.Manifest `json:"manifest"`
}

type User struct {
	ID              string
	AllowedChannels map[string]struct{}
	Manifest        aurora.Manifest
	Digest          string
}

type Set struct {
	users map[string]User
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
	set := &Set{users: make(map[string]User, len(file.Users))}
	for rawID, configured := range file.Users {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, errors.New("policy contains an empty Slack user ID")
		}
		manifest, err := aurora.ValidateManifest(configured.Manifest, provider)
		if err != nil {
			return nil, fmt.Errorf("user %s manifest: %w", id, err)
		}
		channels := make(map[string]struct{}, len(configured.AllowedChannels))
		for _, channelID := range configured.AllowedChannels {
			channelID = strings.TrimSpace(channelID)
			if channelID == "" {
				return nil, fmt.Errorf("user %s has an empty channel ID", id)
			}
			channels[channelID] = struct{}{}
		}
		if len(channels) == 0 {
			return nil, fmt.Errorf("user %s must allow at least one channel", id)
		}
		digestInput, _ := json.Marshal(manifest)
		sum := sha256.Sum256(digestInput)
		set.users[id] = User{
			ID: id, AllowedChannels: channels, Manifest: manifest,
			Digest: hex.EncodeToString(sum[:]),
		}
	}
	return set, nil
}

// Authorize reports whether the Slack user may drive the agent in the channel.
func (s *Set) Authorize(userID, channelID string) (User, bool) {
	user, ok := s.users[userID]
	if !ok {
		return User{}, false
	}
	_, ok = user.AllowedChannels[channelID]
	return user, ok
}
