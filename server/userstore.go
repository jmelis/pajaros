package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// Preferences (preferred language, favorite areas) live in their own
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
// language).
type Profile struct {
	ID                string     `json:"id"`
	Email             string     `json:"email"`
	DisplayName       string     `json:"displayName"`
	Language          string     `json:"language"`
	SecondaryLanguage string     `json:"secondaryLanguage"`
	Favorites         []Favorite `json:"favorites"`
}

// Favorite is a bookmarked area: a place from places.bolt (Key "p<id>") or a
// custom circle (Key "c<lat>,<lng>,<km>"). The name, kind and country are
// captured when the bookmark is added, so the home list renders without a
// places lookup per favorite; Name is the place's own name and may be empty
// for a custom circle. CustomName is the label the user gave the favorite and,
// when set, is what the UI shows instead.
type Favorite struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	CustomName string  `json:"customName,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Region     string  `json:"region,omitempty"` // custom circles only; described on read, not stored
	Country    string  `json:"country,omitempty"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	RadiusKm   float64 `json:"radiusKm"`
}

// schemaColumn is one column this code expects a table to have. def is the
// full SQL definition, used both in the CREATE TABLE and in the ALTER TABLE
// that adds the column to a database created before it existed.
type schemaColumn struct {
	name string
	def  string
}

// schemaTable is one table and the columns the code expects on it.
type schemaTable struct {
	name       string
	columns    []schemaColumn
	primaryKey string // optional table-level PRIMARY KEY (...) clause
}

// schemaTables is the whole database layout, reconciled at startup. It is
// applied additively and idempotently on every boot: a missing table is
// created, and a table that predates a later-added column gets that column
// via ALTER TABLE. No column is ever dropped or rewritten, so pre-existing
// rows and values survive untouched. This is deliberately not a general
// migration framework (see the plan's non-goals).
//
// An older database's user_preferences may still carry retired columns —
// display_mode (the old standard/kid display mode) and star_rewards (the
// retired star-reward toggle). They are left in place, unread and unwritten.
// An older database's user_stats table, once the running star total, is
// likewise left in place, unread and unwritten. So are an older database's
// card_progress (the retired Leitner spaced-repetition state — see Learn
// mode's ARCHITECTURE.md section) and species_taxonomy (durable taxonomy
// that only ever existed to let the also-retired Mastered view resolve
// names offline) tables. Timestamps are stored as Unix nanoseconds.
var schemaTables = []schemaTable{
	{
		name: "users",
		columns: []schemaColumn{
			{"id", "TEXT PRIMARY KEY"},
			{"provider", "TEXT NOT NULL"},
			{"subject", "TEXT NOT NULL"},
			{"email", "TEXT NOT NULL DEFAULT ''"},
			{"display_name", "TEXT NOT NULL DEFAULT ''"},
			{"first_seen", "INTEGER NOT NULL"},
			{"last_seen", "INTEGER NOT NULL"},
		},
	},
	{
		name: "user_preferences",
		columns: []schemaColumn{
			{"user_id", "TEXT PRIMARY KEY"},
			{"language", "TEXT NOT NULL DEFAULT ''"},
			{"secondary_language", "TEXT NOT NULL DEFAULT ''"},
		},
	},
	{
		name: "favorite_areas",
		columns: []schemaColumn{
			{"user_id", "TEXT NOT NULL"},
			{"area_key", "TEXT NOT NULL"},
			{"name", "TEXT NOT NULL DEFAULT ''"},
			{"kind", "TEXT NOT NULL DEFAULT ''"},
			{"country", "TEXT NOT NULL DEFAULT ''"},
			{"lat", "REAL NOT NULL DEFAULT 0"},
			{"lng", "REAL NOT NULL DEFAULT 0"},
			{"radius_km", "REAL NOT NULL DEFAULT 0"},
			{"custom_name", "TEXT NOT NULL DEFAULT ''"},
		},
		primaryKey: "PRIMARY KEY (user_id, area_key)",
	},
}

// createTableSQL builds the CREATE TABLE statement for t.
func createTableSQL(t schemaTable) string {
	defs := make([]string, 0, len(t.columns)+1)
	for _, c := range t.columns {
		defs = append(defs, c.name+" "+c.def)
	}
	if t.primaryKey != "" {
		defs = append(defs, t.primaryKey)
	}
	return "CREATE TABLE IF NOT EXISTS " + t.name + " (" + strings.Join(defs, ", ") + ")"
}

// applySchema reconciles the database against schemaTables: it creates any
// missing table and adds any column an existing table is missing, leaving all
// existing data alone.
func applySchema(db *sql.DB) error {
	// Favorites were once hotspots keyed by coordinates; areas replaced them
	// and the old rows have no equivalent, so the table is dropped.
	if _, err := db.Exec(`DROP TABLE IF EXISTS favorite_hotspots`); err != nil {
		return fmt.Errorf("drop favorite_hotspots: %w", err)
	}
	for _, t := range schemaTables {
		if _, err := db.Exec(createTableSQL(t)); err != nil {
			return fmt.Errorf("create table %s: %w", t.name, err)
		}
		existing, err := tableColumns(db, t.name)
		if err != nil {
			return err
		}
		for _, c := range t.columns {
			if existing[c.name] {
				continue
			}
			if _, err := db.Exec("ALTER TABLE " + t.name + " ADD COLUMN " + c.name + " " + c.def); err != nil {
				return fmt.Errorf("add column %s.%s: %w", t.name, c.name, err)
			}
		}
	}
	return nil
}

// tableColumns returns the set of column names currently on table.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("inspect table %s: %w", table, err)
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

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
	if err := applySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile user schema: %w", err)
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

// maxFavorites bounds how many areas one account can bookmark, keeping both
// the home list and the favorite_areas table finite.
const maxFavorites = 100

// ErrFavoritesLimit is returned by AddFavorite when the account is already at
// maxFavorites and the key is not one of the existing favorites.
var ErrFavoritesLimit = errors.New("favorites limit reached")

// AddFavorite records fav as a favorite of the account. Adding one that is
// already favorited succeeds and replaces its stored details, without
// duplicating the row. Adding a new one when the account is already at
// maxFavorites returns ErrFavoritesLimit.
func (s *UserStore) AddFavorite(id string, fav Favorite) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var existing int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM favorite_areas WHERE user_id = ? AND area_key = ?`, id, fav.Key,
	).Scan(&existing); err != nil {
		return err
	}
	if existing == 0 {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM favorite_areas WHERE user_id = ?`, id,
		).Scan(&count); err != nil {
			return err
		}
		if count >= maxFavorites {
			return ErrFavoritesLimit
		}
	}

	_, err := s.db.Exec(
		`INSERT INTO favorite_areas (user_id, area_key, name, kind, country, lat, lng, radius_km)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, area_key) DO UPDATE SET
			name = excluded.name, kind = excluded.kind, country = excluded.country,
			lat = excluded.lat, lng = excluded.lng, radius_km = excluded.radius_km`,
		id, fav.Key, fav.Name, fav.Kind, fav.Country, fav.Lat, fav.Lng, fav.RadiusKm,
	)
	return err
}

// SetFavoriteName stores the label the user gave a favorite; "" restores the
// place's own name. ok is false when the area is not one of the account's
// favorites.
func (s *UserStore) SetFavoriteName(id, key, name string) (ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE favorite_areas SET custom_name = ? WHERE user_id = ? AND area_key = ?`, name, id, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UserCount returns the total number of accounts. Used only for metrics
// (see metrics.go), so it's fine that this is a full-table COUNT(*) — the
// table is small and it's read at most once per scrape.
func (s *UserStore) UserCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// FavoriteCount returns the total number of favorited areas across every
// account. Metrics-only, see UserCount.
func (s *UserStore) FavoriteCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM favorite_areas`).Scan(&count)
	return count, err
}

// RemoveFavorite deletes key from the account's favorites. Removing one that
// isn't favorited is a no-op.
func (s *UserStore) RemoveFavorite(id, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM favorite_areas WHERE user_id = ? AND area_key = ?`, id, key)
	return err
}

// Favorites returns the account's favorites in a stable (lexicographic by
// key) order. The result is never nil.
func (s *UserStore) Favorites(id string) ([]Favorite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.favorites(id)
}

// favorites is the unlocked read used by Favorites and Profile callers that
// already hold s.mu.
func (s *UserStore) favorites(id string) ([]Favorite, error) {
	rows, err := s.db.Query(
		`SELECT area_key, name, custom_name, kind, country, lat, lng, radius_km FROM favorite_areas WHERE user_id = ? ORDER BY area_key`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	favs := []Favorite{}
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.Key, &f.Name, &f.CustomName, &f.Kind, &f.Country, &f.Lat, &f.Lng, &f.RadiusKm); err != nil {
			return nil, err
		}
		favs = append(favs, f)
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
		        COALESCE((SELECT secondary_language FROM user_preferences WHERE user_id = users.id), '')
		 FROM users WHERE id = ?`, id,
	).Scan(&p.ID, &p.Email, &p.DisplayName, &lang, &p.SecondaryLanguage)
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
