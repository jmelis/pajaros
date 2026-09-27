package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// User is the persisted record this app keeps for a logged-in account. It is
// deliberately tiny: the login gate needs a stable identity to key rate
// limits and sessions on, nothing more (see the plan's non-goals — no profile
// management).
//
// ID is "provider:subject" so the same person signing in through Google and
// Apple stays two distinct accounts, and the same Google account keeps the
// same ID across logins.
type User struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Subject     string    `json:"subject"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

// UserStore persists one small JSON file per user under a directory. That's
// the whole database: the plan explicitly rules out anything heavier. Files
// are named by the SHA-256 of the user ID rather than the ID itself because
// the ID embeds a provider subject (attacker/caller-influenced in principle)
// and must never be used verbatim as a path component.
type UserStore struct {
	dir string
	mu  sync.Mutex
}

func NewUserStore(dir string) (*UserStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create user dir %q: %w", dir, err)
	}
	return &UserStore{dir: dir}, nil
}

func userID(provider, subject string) string {
	return provider + ":" + subject
}

func (s *UserStore) path(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Upsert records a successful login, creating the user on first sight and
// updating last-seen (plus any newly supplied email/display name) afterwards.
func (s *UserStore) Upsert(provider, subject, email, displayName string) (*User, error) {
	if provider == "" || subject == "" {
		return nil, fmt.Errorf("provider and subject are required")
	}
	id := userID(provider, subject)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	path := s.path(id)
	u := &User{
		ID:        id,
		Provider:  provider,
		Subject:   subject,
		FirstSeen: now,
	}
	if existing, err := readUser(path); err == nil {
		u.FirstSeen = existing.FirstSeen
		u.Email = existing.Email
		u.DisplayName = existing.DisplayName
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if email != "" {
		u.Email = email
	}
	if displayName != "" {
		u.DisplayName = displayName
	}
	u.LastSeen = now

	if err := writeUser(path, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Get returns the persisted record for id, or ok=false if it doesn't exist.
func (s *UserStore) Get(id string) (*User, bool, error) {
	u, err := readUser(s.path(id))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

func readUser(path string) (*User, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("parse user file %q: %w", path, err)
	}
	return &u, nil
}

// writeUser writes atomically (temp file + rename) so a crash mid-write can't
// leave a truncated user record behind.
func writeUser(path string, u *User) error {
	data, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".user-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
