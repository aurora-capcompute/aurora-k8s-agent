package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"aurora-capcompute/aurora"
)

type File struct {
	Version int                   `json:"version"`
	Users   map[string]UserPolicy `json:"users"`
}

type UserPolicy struct {
	AllowedChats      []int64            `json:"allowed_chats"`
	Manifest          aurora.Manifest    `json:"manifest"`
	ElevationProfiles map[string]Profile `json:"elevation_profiles,omitempty"`
}

type Profile struct {
	Label       string                    `json:"label"`
	Description string                    `json:"description,omitempty"`
	TTL         string                    `json:"ttl,omitempty"`
	Overrides   []aurora.CapabilityConfig `json:"overrides"`
}

type User struct {
	ID                int64
	AllowedChats      map[int64]struct{}
	Manifest          aurora.Manifest
	ElevationProfiles map[string]ValidatedProfile
	Digest            string
}

type ValidatedProfile struct {
	Name        string
	Label       string
	Description string
	TTL         time.Duration
	Overrides   []aurora.CapabilityConfig
	Effective   aurora.Manifest
}

type Set struct {
	users map[int64]User
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)

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
		profiles := make(map[string]ValidatedProfile, len(configured.ElevationProfiles))
		names := make([]string, 0, len(configured.ElevationProfiles))
		cleanNames := make(map[string]struct{}, len(configured.ElevationProfiles))
		for name := range configured.ElevationProfiles {
			clean := strings.TrimSpace(name)
			if _, exists := cleanNames[clean]; exists {
				return nil, fmt.Errorf("user %d has duplicate elevation profile %q", id, clean)
			}
			cleanNames[clean] = struct{}{}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			profile := configured.ElevationProfiles[name]
			name = strings.TrimSpace(name)
			if !profileNamePattern.MatchString(name) || len(profile.Overrides) == 0 {
				return nil, fmt.Errorf("user %d has invalid empty elevation profile", id)
			}
			seenCapabilities := make(map[string]struct{}, len(profile.Overrides))
			for _, override := range profile.Overrides {
				if _, exists := seenCapabilities[override.Name]; exists {
					return nil, fmt.Errorf("user %d profile %q repeats capability %q", id, name, override.Name)
				}
				seenCapabilities[override.Name] = struct{}{}
			}
			effective, err := aurora.EffectiveManifest(manifest, profile.Overrides, provider)
			if err != nil {
				return nil, fmt.Errorf("user %d profile %q: %w", id, name, err)
			}
			ttl := 10 * time.Minute
			if profile.TTL != "" {
				ttl, err = time.ParseDuration(profile.TTL)
				if err != nil || ttl <= 0 {
					return nil, fmt.Errorf("user %d profile %q has invalid ttl", id, name)
				}
			}
			label := strings.TrimSpace(profile.Label)
			if label == "" {
				label = name
			}
			normalized := make([]aurora.CapabilityConfig, 0, len(profile.Overrides))
			for _, override := range profile.Overrides {
				for _, capability := range effective.Capabilities {
					if capability.Name == override.Name {
						normalized = append(normalized, capability)
						break
					}
				}
			}
			profiles[name] = ValidatedProfile{
				Name: name, Label: label, Description: strings.TrimSpace(profile.Description),
				TTL: ttl, Overrides: normalized, Effective: effective,
			}
		}
		digestInput, _ := json.Marshal(struct {
			Manifest aurora.Manifest
			Profiles map[string]ValidatedProfile
		}{manifest, profiles})
		sum := sha256.Sum256(digestInput)
		set.users[id] = User{
			ID: id, AllowedChats: chats, Manifest: manifest,
			ElevationProfiles: profiles, Digest: hex.EncodeToString(sum[:]),
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
