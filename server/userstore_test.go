package main

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a store backed by a fresh database file and registers
// cleanup. It also returns the path so tests can reopen the same file.
func newTestStore(t *testing.T) (*UserStore, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "users.db")
	store, err := NewUserStore(dbPath)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dbPath
}

func TestUserStoreCreatesThenUpdates(t *testing.T) {
	store, _ := newTestStore(t)

	first, err := store.Upsert("google", "subject-1", "a@example.com", "Alice")
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if first.ID != "google:subject-1" {
		t.Errorf("ID = %q, want google:subject-1", first.ID)
	}
	if !first.FirstSeen.Equal(first.LastSeen) {
		t.Errorf("first login: FirstSeen %v != LastSeen %v", first.FirstSeen, first.LastSeen)
	}

	time.Sleep(2 * time.Millisecond)
	second, err := store.Upsert("google", "subject-1", "a@example.com", "Alice")
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if !second.FirstSeen.Equal(first.FirstSeen) {
		t.Errorf("FirstSeen changed on second login: %v -> %v", first.FirstSeen, second.FirstSeen)
	}
	if !second.LastSeen.After(first.LastSeen) {
		t.Errorf("LastSeen not advanced: %v -> %v", first.LastSeen, second.LastSeen)
	}

	// A second login must update the existing row, not insert a duplicate.
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Errorf("user rows = %d, want 1", count)
	}
}

func TestUserStoreDistinguishesProviders(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Upsert("google", "same-sub", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert("apple", "same-sub", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get("google:same-sub"); !ok {
		t.Error("google record missing")
	}
	if _, ok, _ := store.Get("apple:same-sub"); !ok {
		t.Error("apple record missing")
	}
}

func TestUserStorePersistsAcrossInstances(t *testing.T) {
	store, dbPath := newTestStore(t)
	before, err := store.Upsert("google", "sub", "x@y.z", "X")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewUserStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewUserStore: %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.Get("google:sub")
	if err != nil || !ok {
		t.Fatalf("Get after reopen: (%v, %v, %v)", got, ok, err)
	}
	if got.Email != "x@y.z" || !got.FirstSeen.Equal(before.FirstSeen) {
		t.Errorf("persisted record mismatch: %+v", got)
	}
}

func TestUserStoreGetMissing(t *testing.T) {
	store, _ := newTestStore(t)
	_, ok, err := store.Get("google:nope")
	if err != nil || ok {
		t.Fatalf("Get missing = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestUserStoreCreatesSchemaInFreshLocation(t *testing.T) {
	// A path whose parent directory doesn't exist yet: the store must create
	// both the directory and the schema with no manual setup step.
	dbPath := filepath.Join(t.TempDir(), "nested", "dir", "users.db")
	store, err := NewUserStore(dbPath)
	if err != nil {
		t.Fatalf("NewUserStore in fresh location: %v", err)
	}
	defer store.Close()

	if _, err := store.Upsert("google", "fresh", "fresh@example.com", "Fresh"); err != nil {
		t.Fatalf("Upsert against fresh database: %v", err)
	}
}

func TestUserStoreLanguagePreferencePersists(t *testing.T) {
	store, dbPath := newTestStore(t)
	if _, err := store.Upsert("google", "sub", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:sub"

	if lang, ok, err := store.PreferredLanguage(id); err != nil || ok || lang != "" {
		t.Fatalf("initial PreferredLanguage = (%q, %v, %v), want (\"\", false, nil)", lang, ok, err)
	}
	if err := store.SetLanguage(id, "fr"); err != nil {
		t.Fatalf("SetLanguage: %v", err)
	}
	if lang, ok, err := store.PreferredLanguage(id); err != nil || !ok || lang != "fr" {
		t.Fatalf("PreferredLanguage = (%q, %v, %v), want (\"fr\", true, nil)", lang, ok, err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewUserStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewUserStore: %v", err)
	}
	defer reopened.Close()
	if lang, ok, err := reopened.PreferredLanguage(id); err != nil || !ok || lang != "fr" {
		t.Fatalf("PreferredLanguage after reopen = (%q, %v, %v), want (\"fr\", true, nil)", lang, ok, err)
	}
}

func TestUserStoreFavoritesLifecyclePersists(t *testing.T) {
	store, dbPath := newTestStore(t)
	if _, err := store.Upsert("google", "sub", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:sub"

	if favs, err := store.Favorites(id); err != nil || len(favs) != 0 {
		t.Fatalf("initial Favorites = (%v, %v), want empty", favs, err)
	}

	for _, locID := range []string{"L2", "L1", "L1"} { // L1 twice: duplicate is a no-op
		if err := store.AddFavorite(id, locID); err != nil {
			t.Fatalf("AddFavorite(%s): %v", locID, err)
		}
	}
	favs, err := store.Favorites(id)
	if err != nil {
		t.Fatalf("Favorites: %v", err)
	}
	if want := []string{"L1", "L2"}; len(favs) != len(want) || favs[0] != want[0] || favs[1] != want[1] {
		t.Fatalf("Favorites = %v, want %v (duplicate must not create a second entry)", favs, want)
	}

	if err := store.RemoveFavorite(id, "L1"); err != nil {
		t.Fatalf("RemoveFavorite: %v", err)
	}
	if err := store.RemoveFavorite(id, "L404"); err != nil { // removing a non-favorite is a no-op
		t.Fatalf("RemoveFavorite(non-favorite): %v", err)
	}
	favs, err = store.Favorites(id)
	if err != nil {
		t.Fatalf("Favorites after remove: %v", err)
	}
	if len(favs) != 1 || favs[0] != "L2" {
		t.Fatalf("Favorites after remove = %v, want [L2]", favs)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewUserStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewUserStore: %v", err)
	}
	defer reopened.Close()
	favs, err = reopened.Favorites(id)
	if err != nil {
		t.Fatalf("Favorites after reopen: %v", err)
	}
	if len(favs) != 1 || favs[0] != "L2" {
		t.Fatalf("Favorites after reopen = %v, want [L2]", favs)
	}
}

func TestUserStoreProfileDefaultsThenReflectsPreferences(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Upsert("google", "sub", "p@example.com", "Pat"); err != nil {
		t.Fatal(err)
	}
	const id = "google:sub"

	p, ok, err := store.Profile(id)
	if err != nil || !ok {
		t.Fatalf("Profile = (ok=%v, err=%v)", ok, err)
	}
	if p.ID != id || p.Email != "p@example.com" || p.DisplayName != "Pat" {
		t.Errorf("profile identity = %+v", p)
	}
	if p.Language != defaultLang {
		t.Errorf("default Language = %q, want %q", p.Language, defaultLang)
	}
	if p.Favorites == nil || len(p.Favorites) != 0 {
		t.Errorf("default Favorites = %v, want empty non-nil", p.Favorites)
	}

	if err := store.SetLanguage(id, "en"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddFavorite(id, "L42"); err != nil {
		t.Fatal(err)
	}
	p, ok, err = store.Profile(id)
	if err != nil || !ok {
		t.Fatalf("Profile after update = (ok=%v, err=%v)", ok, err)
	}
	if p.Language != "en" {
		t.Errorf("Language = %q, want en", p.Language)
	}
	if len(p.Favorites) != 1 || p.Favorites[0] != "L42" {
		t.Errorf("Favorites = %v, want [L42]", p.Favorites)
	}

	if _, ok, err := store.Profile("google:missing"); err != nil || ok {
		t.Fatalf("Profile missing = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
