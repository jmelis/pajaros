package main

import (
	"database/sql"
	"encoding/json"
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
// language). SpeciesMastered summarizes the account's quiz progress; see
// ProgressStats.
type Profile struct {
	ID                string     `json:"id"`
	Email             string     `json:"email"`
	DisplayName       string     `json:"displayName"`
	Language          string     `json:"language"`
	SecondaryLanguage string     `json:"secondaryLanguage"`
	SpeciesMastered   int        `json:"speciesMastered"`
	Favorites         []Favorite `json:"favorites"`
}

// Favorite is a bookmarked hotspot. LocName and the coordinates are captured
// when the bookmark is added (the client already has them from the hotspot it
// is viewing or from a search result), so the home list can render a name and
// place the hotspot on a map without a live eBird call per favorite. Lat/Lng
// are 0 when unknown (e.g. a favorite migrated from the id-only rows the
// store used to keep). LocName may be "" when it could not be resolved; the
// frontend then falls back to the location id.
type Favorite struct {
	LocID   string  `json:"locId"`
	LocName string  `json:"locName"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// MasteredSpecies is one box-5 card for the mastered view, enriched with the
// names and photo the view needs. ComName is resolved in the requested
// language (falling back to another recorded language, then the scientific
// name, then the species code) so a bird never vanishes from the list for
// lack of a localized name. SecondaryName is the same resolution in the
// account's secondary subtitle language, or "" when none is set.
type MasteredSpecies struct {
	SpeciesCode   string `json:"speciesCode"`
	ComName       string `json:"comName"`
	SecondaryName string `json:"secondaryName"`
	SciName       string `json:"sciName"`
	ImageURL      string `json:"imageUrl,omitempty"`
	ImageMissing  bool   `json:"imageMissing,omitempty"`
	Box           int    `json:"box"`
	SeenCount     int    `json:"seenCount"`
	CorrectCount  int    `json:"correctCount"`
	DueAt         int64  `json:"dueAt"`
	LastSeenAt    int64  `json:"lastSeenAt"`
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

// ProgressStats summarizes an account's quiz progress for the home and
// settings views. Mastered counts box-5 cards; Learning counts every other
// card that has a progress row (boxes 1-4); DueForReview counts cards whose
// due time has arrived, mastered or not. The caller passes now in (rather
// than the store reading the clock) so the summary stays testable.
type ProgressStats struct {
	SpeciesMastered int `json:"speciesMastered"`
	SpeciesLearning int `json:"speciesLearning"`
	DueForReview    int `json:"dueForReview"`
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
// likewise left in place, unread and unwritten. Timestamps are stored as Unix
// nanoseconds.
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
		name: "favorite_hotspots",
		columns: []schemaColumn{
			{"user_id", "TEXT NOT NULL"},
			{"loc_id", "TEXT NOT NULL"},
			{"loc_name", "TEXT NOT NULL DEFAULT ''"},
			{"lat", "REAL NOT NULL DEFAULT 0"},
			{"lng", "REAL NOT NULL DEFAULT 0"},
		},
		primaryKey: "PRIMARY KEY (user_id, loc_id)",
	},
	{
		name: "card_progress",
		columns: []schemaColumn{
			{"user_id", "TEXT NOT NULL"},
			{"species_code", "TEXT NOT NULL"},
			{"box", "INTEGER NOT NULL DEFAULT 0"},
			{"due_at", "INTEGER NOT NULL DEFAULT 0"},
			{"seen_count", "INTEGER NOT NULL DEFAULT 0"},
			{"correct_count", "INTEGER NOT NULL DEFAULT 0"},
			{"last_seen_at", "INTEGER NOT NULL DEFAULT 0"},
		},
		primaryKey: "PRIMARY KEY (user_id, species_code)",
	},
	{
		// Durable taxonomy resolved from eBird on hotspot species loads and
		// quiz builds, so the mastered view can name a bird without an
		// upstream call. names is a JSON object mapping language code to that
		// language's common name.
		name: "species_taxonomy",
		columns: []schemaColumn{
			{"species_code", "TEXT PRIMARY KEY"},
			{"sci_name", "TEXT NOT NULL DEFAULT ''"},
			{"names", "TEXT NOT NULL DEFAULT '{}'"},
		},
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

// maxFavorites bounds how many hotspots one account can bookmark, keeping
// both the home list and the favorite_hotspots table finite.
const maxFavorites = 100

// ErrFavoritesLimit is returned by AddFavorite when the account is already at
// maxFavorites and locID is not one of the existing favorites.
var ErrFavoritesLimit = errors.New("favorites limit reached")

// AddFavorite records fav as a favorite of the account. Adding one that is
// already favorited succeeds and refreshes its stored name/coordinates when
// the caller supplied them, so an older id-only favorite can be backfilled
// without duplicating the row. Adding a new one when the account is already
// at maxFavorites returns ErrFavoritesLimit.
func (s *UserStore) AddFavorite(id string, fav Favorite) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var existing int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM favorite_hotspots WHERE user_id = ? AND loc_id = ?`, id, fav.LocID,
	).Scan(&existing); err != nil {
		return err
	}
	if existing == 0 {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM favorite_hotspots WHERE user_id = ?`, id,
		).Scan(&count); err != nil {
			return err
		}
		if count >= maxFavorites {
			return ErrFavoritesLimit
		}
	}

	// Empty/zero fields are treated as "leave what's there": a re-add with no
	// body stays the no-op it always was, and an id-only legacy row keeps its
	// values unless a caller supplies better ones.
	_, err := s.db.Exec(
		`INSERT INTO favorite_hotspots (user_id, loc_id, loc_name, lat, lng)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, loc_id) DO UPDATE SET
			loc_name = CASE WHEN excluded.loc_name <> '' THEN excluded.loc_name ELSE favorite_hotspots.loc_name END,
			lat      = CASE WHEN excluded.lat <> 0 OR excluded.lng <> 0 THEN excluded.lat ELSE favorite_hotspots.lat END,
			lng      = CASE WHEN excluded.lat <> 0 OR excluded.lng <> 0 THEN excluded.lng ELSE favorite_hotspots.lng END`,
		id, fav.LocID, fav.LocName, fav.Lat, fav.Lng,
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

// Favorites returns the account's favorites in a stable (lexicographic by
// location id) order. The result is never nil.
func (s *UserStore) Favorites(id string) ([]Favorite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.favorites(id)
}

// favorites is the unlocked read used by Favorites and Profile callers that
// already hold s.mu.
func (s *UserStore) favorites(id string) ([]Favorite, error) {
	rows, err := s.db.Query(
		`SELECT loc_id, loc_name, lat, lng FROM favorite_hotspots WHERE user_id = ? ORDER BY loc_id`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	favs := []Favorite{}
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.LocID, &f.LocName, &f.Lat, &f.Lng); err != nil {
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
		        COALESCE((SELECT secondary_language FROM user_preferences WHERE user_id = users.id), ''),
		        (SELECT COUNT(*) FROM card_progress WHERE user_id = users.id AND box >= 5)
		 FROM users WHERE id = ?`, id,
	).Scan(&p.ID, &p.Email, &p.DisplayName, &lang, &p.SecondaryLanguage, &p.SpeciesMastered)
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
// Leitner box/due date. now is passed in (rather than read here) so callers
// and tests control the clock. The returned CardAnswer reports the
// post-answer state.
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

	box, dueAt := leitnerTransition(prevBox, correct, now)
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
	return CardAnswer{
		Box:   box,
		DueAt: dueAt.UnixNano(),
	}, nil
}

// ProgressStats summarizes the account's quiz progress for the home and
// settings views. now is passed in so the "due for review" count is
// deterministic under test.
func (s *UserStore) ProgressStats(id string, now time.Time) (ProgressStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var st ProgressStats
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
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM card_progress WHERE user_id = ? AND due_at <= ?`, id, now.UnixNano(),
	).Scan(&st.DueForReview); err != nil {
		return st, err
	}
	return st, nil
}

// ResetProgress clears every progress row for id. This is the "reset my
// progress" action; it is not reversible.
func (s *UserStore) ResetProgress(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM card_progress WHERE user_id = ?`, id)
	return err
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

// RecordTaxa merges freshly resolved taxonomy into species_taxonomy for the
// given language. It is global (not per-account): a species' name is the same
// everywhere. The merge is additive — a later language never drops names
// recorded earlier — so the mastered view can fall back across languages.
// Callers treat a failure as non-fatal: this is durable caching of data the
// request already needed, not a request precondition.
func (s *UserStore) RecordTaxa(taxa map[string]Taxon, lang string) error {
	if len(taxa) == 0 || lang == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for code, t := range taxa {
		if code == "" {
			continue
		}
		names := map[string]string{}
		var sci, raw string
		err := tx.QueryRow(
			`SELECT sci_name, names FROM species_taxonomy WHERE species_code = ?`, code,
		).Scan(&sci, &raw)
		switch {
		case err == nil:
			_ = json.Unmarshal([]byte(raw), &names)
		case errors.Is(err, sql.ErrNoRows):
			// New species: start empty.
		default:
			return err
		}
		if t.ComName != "" {
			names[lang] = t.ComName
		}
		if t.SciName != "" {
			sci = t.SciName
		}
		encoded, err := json.Marshal(names)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO species_taxonomy (species_code, sci_name, names) VALUES (?, ?, ?)
			 ON CONFLICT(species_code) DO UPDATE SET
				sci_name = excluded.sci_name,
				names    = excluded.names`,
			code, sci, string(encoded),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MasteredSpecies returns every box-5 card for id, each resolved with a
// display name in lang (and secondaryLang when non-empty) from the durable
// taxonomy store. It reads only local data — no upstream call. A species with
// no stored name at all still appears, named by its scientific name or its
// code, so the list never loses a mastered bird.
func (s *UserStore) MasteredSpecies(id, lang, secondaryLang string) ([]MasteredSpecies, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// One JOIN rather than a taxonomy lookup per row: with a single pooled
	// connection, a nested query while iterating rows would deadlock.
	rows, err := s.db.Query(
		`SELECT p.species_code, p.box, p.due_at, p.seen_count, p.correct_count, p.last_seen_at,
		        COALESCE(t.sci_name, ''), COALESCE(t.names, '{}')
		 FROM card_progress p
		 LEFT JOIN species_taxonomy t ON t.species_code = p.species_code
		 WHERE p.user_id = ? AND p.box >= 5
		 ORDER BY p.species_code`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MasteredSpecies{}
	for rows.Next() {
		var m MasteredSpecies
		var sci, raw string
		if err := rows.Scan(
			&m.SpeciesCode, &m.Box, &m.DueAt, &m.SeenCount, &m.CorrectCount, &m.LastSeenAt,
			&sci, &raw,
		); err != nil {
			return nil, err
		}
		m.SciName = sci
		names := map[string]string{}
		if json.Unmarshal([]byte(raw), &names) == nil {
			m.ComName = pickName(names, lang)
			if secondaryLang != "" {
				m.SecondaryName = names[secondaryLang]
			}
		}
		if m.ComName == "" {
			m.ComName = m.SciName
		}
		if m.ComName == "" {
			m.ComName = m.SpeciesCode
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// pickName resolves a species' display name for lang: the requested language
// first, then English, then any recorded language. Returns "" when no name is
// known.
func pickName(names map[string]string, lang string) string {
	if name := names[lang]; name != "" {
		return name
	}
	if name := names["en"]; name != "" {
		return name
	}
	for _, name := range names {
		if name != "" {
			return name
		}
	}
	return ""
}
