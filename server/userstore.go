package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// User is the persisted identity record this app keeps for a logged-in
// account. It is deliberately tiny: the login gate needs a stable identity to
// key rate limits and sessions on, plus the profile fields providers hand us
// at login.
//
// ID is "provider:subject" so the same person signing in through Google and
// Apple stays two distinct accounts, and the same Google account keeps the
// same ID across logins.
//
// Preferences (preferred language, favorite hotspots) live in their own
// tables alongside this one; see UserStore and Profile.
type User struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Subject     string    `json:"subject"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

// Profile is the identity plus current preferences the account's own profile
// endpoint returns. Language is always the effective value (the stored
// preference, or defaultLang when unset) and Favorites is never nil, so a
// brand-new account renders as a clean default rather than JSON nulls.
type Profile struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	Language    string   `json:"language"`
	Favorites   []string `json:"favorites"`
}

// schema is the whole database layout, applied idempotently at startup. There
// is no migration framework yet (see the plan's non-goals): tables are created
// if absent, so starting against a fresh, empty file just works. Timestamps
// are stored as Unix nanoseconds.
const schema = `
CREATE TABLE IF NOT EXISTS users (
	id           TEXT PRIMARY KEY,
	provider     TEXT NOT NULL,
	subject      TEXT NOT NULL,
	email        TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	first_seen   INTEGER NOT NULL,
	last_seen    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_preferences (
	user_id  TEXT PRIMARY KEY,
	language TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS favorite_hotspots (
	user_id TEXT NOT NULL,
	loc_id  TEXT NOT NULL,
	PRIMARY KEY (user_id, loc_id)
);
`

// UserStore is the app's persistence layer: an identity row per account plus
// that account's preferences, all in SQLite. It replaces the earlier
// one-JSON-file-per-user store; callers still speak the same Upsert/Get
// vocabulary (see auth.go).
//
// Access is serialized with a mutex and a single pooled connection. SQLite
// allows only one writer anyway, and this app's queries are tiny, so this
// avoids SQLITE_BUSY between concurrent HTTP handlers at the cost of nothing
// we'd notice.
type UserStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewUserStore opens (creating if needed) the SQLite database at dbPath,
// configures it, and applies the schema. It never requires a manual setup
// step.
func NewUserStore(dbPath string) (*UserStore, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("user database path is required")
	}
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database dir %q: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open user database %q: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure user database: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create user schema: %w", err)
	}
	return &UserStore{db: db}, nil
}

// Close releases the database handle. Safe to call once; further use after
// Close is a programming error.
func (s *UserStore) Close() error {
	return s.db.Close()
}

func userID(provider, subject string) string {
	return provider + ":" + subject
}

// Upsert records a successful login, creating the user on first sight and
// updating last-seen (plus any newly supplied email/display name) afterwards.
// A second login for the same account updates the existing row rather than
// inserting a duplicate.
func (s *UserStore) Upsert(provider, subject, email, displayName string) (*User, error) {
	if provider == "" || subject == "" {
		return nil, fmt.Errorf("provider and subject are required")
	}
	id := userID(provider, subject)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	u := &User{
		ID:        id,
		Provider:  provider,
		Subject:   subject,
		FirstSeen: now,
	}

	var firstSeenNS int64
	err := s.db.QueryRow(
		`SELECT first_seen, email, display_name FROM users WHERE id = ?`, id,
	).Scan(&firstSeenNS, &u.Email, &u.DisplayName)
	switch {
	case err == nil:
		u.FirstSeen = time.Unix(0, firstSeenNS).UTC()
	case errors.Is(err, sql.ErrNoRows):
		// First login: nothing to carry over.
	default:
		return nil, err
	}

	// Providers only resend email/display name sometimes, so an empty value
	// on a later login must not wipe what we already have.
	if email != "" {
		u.Email = email
	}
	if displayName != "" {
		u.DisplayName = displayName
	}
	u.LastSeen = now

	_, err = s.db.Exec(
		`INSERT INTO users (id, provider, subject, email, display_name, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			last_seen    = excluded.last_seen,
			email        = excluded.email,
			display_name = excluded.display_name`,
		u.ID, u.Provider, u.Subject, u.Email, u.DisplayName,
		u.FirstSeen.UnixNano(), u.LastSeen.UnixNano(),
	)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// Get returns the persisted identity record for id, or ok=false if it doesn't
// exist.
func (s *UserStore) Get(id string) (*User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, err := s.get(id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

// get is the unlocked read used by Get and Profile callers that already hold
// s.mu.
func (s *UserStore) get(id string) (*User, error) {
	var (
		u                       User
		firstSeenNS, lastSeenNS int64
	)
	err := s.db.QueryRow(
		`SELECT id, provider, subject, email, display_name, first_seen, last_seen
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Provider, &u.Subject, &u.Email, &u.DisplayName, &firstSeenNS, &lastSeenNS)
	if err != nil {
		return nil, err
	}
	u.FirstSeen = time.Unix(0, firstSeenNS).UTC()
	u.LastSeen = time.Unix(0, lastSeenNS).UTC()
	return &u, nil
}

// PreferredLanguage returns the account's stored language preference and
// whether one has actually been set. A missing row or empty value means the
// account has no preference, so callers should fall back to defaultLang.
func (s *UserStore) PreferredLanguage(id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lang string
	err := s.db.QueryRow(`SELECT language FROM user_preferences WHERE user_id = ?`, id).Scan(&lang)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if lang == "" {
		return "", false, nil
	}
	return lang, true, nil
}

// SetLanguage stores the account's preferred bird-name language. Callers must
// have validated lang against the same allow-list the request handlers use.
func (s *UserStore) SetLanguage(id, lang string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO user_preferences (user_id, language) VALUES (?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET language = excluded.language`,
		id, lang,
	)
	return err
}

// AddFavorite records locID as a favorite of the account. Adding one that is
// already favorited is a no-op, not an error or a duplicate.
func (s *UserStore) AddFavorite(id, locID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO favorite_hotspots (user_id, loc_id) VALUES (?, ?)
		 ON CONFLICT(user_id, loc_id) DO NOTHING`,
		id, locID,
	)
	return err
}

// RemoveFavorite deletes locID from the account's favorites. Removing one that
// isn't favorited is a no-op.
func (s *UserStore) RemoveFavorite(id, locID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM favorite_hotspots WHERE user_id = ? AND loc_id = ?`, id, locID)
	return err
}

// Favorites returns the account's favorite hotspot ids in a stable
// (lexicographic) order. The result is never nil.
func (s *UserStore) Favorites(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.favorites(id)
}

// favorites is the unlocked read used by Favorites and Profile callers that
// already hold s.mu.
func (s *UserStore) favorites(id string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT loc_id FROM favorite_hotspots WHERE user_id = ? ORDER BY loc_id`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	favs := []string{}
	for rows.Next() {
		var locID string
		if err := rows.Scan(&locID); err != nil {
			return nil, err
		}
		favs = append(favs, locID)
	}
	return favs, rows.Err()
}

// Profile returns the account's identity and current preferences in one call.
// ok=false means no such account. Language is resolved to the effective
// value and Favorites is an empty (non-nil) slice when there are none.
func (s *UserStore) Profile(id string) (*Profile, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		p    Profile
		lang string
	)
	err := s.db.QueryRow(
		`SELECT id, email, display_name,
		        COALESCE((SELECT language FROM user_preferences WHERE user_id = users.id), '')
		 FROM users WHERE id = ?`, id,
	).Scan(&p.ID, &p.Email, &p.DisplayName, &lang)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	p.Language = lang
	if p.Language == "" {
		p.Language = defaultLang
	}
	p.Favorites, err = s.favorites(id)
	if err != nil {
		return nil, false, err
	}
	return &p, true, nil
}
