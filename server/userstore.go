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
//
// SecondaryLanguage is "" when unset (the app's optional second subtitle
// language). DisplayMode is "standard" or "kid". Stars and SpeciesMastered
// summarize the account's quiz progress; see ProgressStats.
type Profile struct {
	ID                string   `json:"id"`
	Email             string   `json:"email"`
	DisplayName       string   `json:"displayName"`
	Language          string   `json:"language"`
	SecondaryLanguage string   `json:"secondaryLanguage"`
	DisplayMode       string   `json:"displayMode"`
	Stars             int      `json:"stars"`
	SpeciesMastered   int      `json:"speciesMastered"`
	Favorites         []string `json:"favorites"`
}

// CardProgress is one account's Leitner state for a single species code. Card
// identity is the eBird species code, global per account (not per hotspot), so
// a species learned at one hotspot counts as learned everywhere.
//
// Box 0 means "row exists but never answered" — in practice rows are only
// created by answering, so a real row is box 1-5. DueAt is Unix nanoseconds;
// 0 means due now.
type CardProgress struct {
	SpeciesCode  string
	Box          int
	DueAt        int64
	SeenCount    int
	CorrectCount int
	LastSeenAt   int64
}

// ProgressStats summarizes an account's quiz progress for the settings view.
// Mastered counts box-5 cards; Learning counts every other card that has a
// progress row (boxes 1-4).
type ProgressStats struct {
	TotalStars      int `json:"totalStars"`
	SpeciesMastered int `json:"speciesMastered"`
	SpeciesLearning int `json:"speciesLearning"`
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
	user_id            TEXT PRIMARY KEY,
	language           TEXT NOT NULL DEFAULT '',
	secondary_language TEXT NOT NULL DEFAULT '',
	display_mode       TEXT NOT NULL DEFAULT 'standard'
);

CREATE TABLE IF NOT EXISTS favorite_hotspots (
	user_id TEXT NOT NULL,
	loc_id  TEXT NOT NULL,
	PRIMARY KEY (user_id, loc_id)
);

CREATE TABLE IF NOT EXISTS card_progress (
	user_id       TEXT NOT NULL,
	species_code  TEXT NOT NULL,
	box           INTEGER NOT NULL DEFAULT 0,
	due_at        INTEGER NOT NULL DEFAULT 0,
	seen_count    INTEGER NOT NULL DEFAULT 0,
	correct_count INTEGER NOT NULL DEFAULT 0,
	last_seen_at  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, species_code)
);

CREATE TABLE IF NOT EXISTS user_stats (
	user_id TEXT PRIMARY KEY,
	stars   INTEGER NOT NULL DEFAULT 0
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
		        COALESCE((SELECT language FROM user_preferences WHERE user_id = users.id), ''),
		        COALESCE((SELECT secondary_language FROM user_preferences WHERE user_id = users.id), ''),
		        COALESCE((SELECT display_mode FROM user_preferences WHERE user_id = users.id), ''),
		        COALESCE((SELECT stars FROM user_stats WHERE user_id = users.id), 0),
		        (SELECT COUNT(*) FROM card_progress WHERE user_id = users.id AND box >= 5)
		 FROM users WHERE id = ?`, id,
	).Scan(&p.ID, &p.Email, &p.DisplayName, &lang, &p.SecondaryLanguage, &p.DisplayMode, &p.Stars, &p.SpeciesMastered)
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
	if p.DisplayMode == "" {
		p.DisplayMode = defaultDisplayMode
	}
	p.Favorites, err = s.favorites(id)
	if err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// AllCardProgress returns every progress row for id, keyed by species code.
// Quiz session composition works on the whole map at once rather than one
// query per species.
func (s *UserStore) AllCardProgress(id string) (map[string]CardProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT species_code, box, due_at, seen_count, correct_count, last_seen_at
		 FROM card_progress WHERE user_id = ?`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]CardProgress)
	for rows.Next() {
		var c CardProgress
		if err := rows.Scan(&c.SpeciesCode, &c.Box, &c.DueAt, &c.SeenCount, &c.CorrectCount, &c.LastSeenAt); err != nil {
			return nil, err
		}
		out[c.SpeciesCode] = c
	}
	return out, rows.Err()
}

// AnswerCard applies one answer to speciesCode for id, updating the card's
// Leitner box/due date and adding any earned stars to the account's running
// total. now is passed in (rather than read here) so callers and tests control
// the clock. The returned CardAnswer reports the post-answer state.
func (s *UserStore) AnswerCard(id, speciesCode string, correct bool, now time.Time) (CardAnswer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var prev CardProgress
	prevBox := 0
	err := s.db.QueryRow(
		`SELECT species_code, box, due_at, seen_count, correct_count, last_seen_at
		 FROM card_progress WHERE user_id = ? AND species_code = ?`, id, speciesCode,
	).Scan(&prev.SpeciesCode, &prev.Box, &prev.DueAt, &prev.SeenCount, &prev.CorrectCount, &prev.LastSeenAt)
	switch {
	case err == nil:
		prevBox = prev.Box
	case errors.Is(err, sql.ErrNoRows):
		// First answer for this card: box 0 ("never answered").
	default:
		return CardAnswer{}, err
	}

	box, dueAt, earned, newlyMastered := leitnerTransition(prevBox, correct, now)
	seen := prev.SeenCount + 1
	correctCount := prev.CorrectCount
	if correct {
		correctCount++
	}
	_, err = s.db.Exec(
		`INSERT INTO card_progress
			(user_id, species_code, box, due_at, seen_count, correct_count, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, species_code) DO UPDATE SET
			box           = excluded.box,
			due_at        = excluded.due_at,
			seen_count    = excluded.seen_count,
			correct_count = excluded.correct_count,
			last_seen_at  = excluded.last_seen_at`,
		id, speciesCode, box, dueAt.UnixNano(), seen, correctCount, now.UnixNano(),
	)
	if err != nil {
		return CardAnswer{}, err
	}
	if earned > 0 {
		_, err = s.db.Exec(
			`INSERT INTO user_stats (user_id, stars) VALUES (?, ?)
			 ON CONFLICT(user_id) DO UPDATE SET stars = stars + excluded.stars`,
			id, earned,
		)
		if err != nil {
			return CardAnswer{}, err
		}
	}
	total, err := s.stars(id)
	if err != nil {
		return CardAnswer{}, err
	}
	return CardAnswer{
		Box:           box,
		DueAt:         dueAt.UnixNano(),
		StarsEarned:   earned,
		TotalStars:    total,
		NewlyMastered: newlyMastered,
	}, nil
}

// stars returns the account's running star total, 0 when it has never earned
// any. Unlocked read; callers must hold s.mu.
func (s *UserStore) stars(id string) (int, error) {
	var stars int
	err := s.db.QueryRow(`SELECT stars FROM user_stats WHERE user_id = ?`, id).Scan(&stars)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return stars, err
}

// ProgressStats summarizes the account's quiz progress for the settings view.
func (s *UserStore) ProgressStats(id string) (ProgressStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var st ProgressStats
	var err error
	if st.TotalStars, err = s.stars(id); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM card_progress WHERE user_id = ? AND box >= 5`, id,
	).Scan(&st.SpeciesMastered); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM card_progress WHERE user_id = ? AND box >= 1 AND box < 5`, id,
	).Scan(&st.SpeciesLearning); err != nil {
		return st, err
	}
	return st, nil
}

// ResetProgress clears every progress row for id and resets its star total to
// zero. This is the "reset my progress" action; it is not reversible.
func (s *UserStore) ResetProgress(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM card_progress WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_stats WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SecondaryLanguage returns the account's optional second subtitle language,
// or "" when it has never been set.
func (s *UserStore) SecondaryLanguage(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lang string
	err := s.db.QueryRow(
		`SELECT secondary_language FROM user_preferences WHERE user_id = ?`, id,
	).Scan(&lang)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return lang, err
}

// SetSecondaryLanguage stores (or, for "", clears) the account's optional
// second subtitle language. Callers must have validated lang against the same
// allow-list the request handlers use, or be clearing it with "".
func (s *UserStore) SetSecondaryLanguage(id, lang string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO user_preferences (user_id, secondary_language) VALUES (?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET secondary_language = excluded.secondary_language`,
		id, lang,
	)
	return err
}

// DisplayMode returns the account's stored display mode, defaulting to
// defaultDisplayMode when it has never been set.
func (s *UserStore) DisplayMode(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var mode string
	err := s.db.QueryRow(
		`SELECT display_mode FROM user_preferences WHERE user_id = ?`, id,
	).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) || mode == "" {
		return defaultDisplayMode, nil
	}
	return mode, err
}

// SetDisplayMode stores the account's display mode. Callers must have
// validated mode against validDisplayModes.
func (s *UserStore) SetDisplayMode(id, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO user_preferences (user_id, display_mode) VALUES (?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET display_mode = excluded.display_mode`,
		id, mode,
	)
	return err
}
