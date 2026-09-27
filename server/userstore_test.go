package main

import (
	"testing"
	"time"
)

func TestUserStoreCreatesThenUpdates(t *testing.T) {
	store, err := NewUserStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}

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
}

func TestUserStoreDistinguishesProviders(t *testing.T) {
	store, _ := NewUserStore(t.TempDir())
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
	dir := t.TempDir()
	store, _ := NewUserStore(dir)
	before, err := store.Upsert("google", "sub", "x@y.z", "X")
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewUserStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.Get("google:sub")
	if err != nil || !ok {
		t.Fatalf("Get after reopen: (%v, %v, %v)", got, ok, err)
	}
	if got.Email != "x@y.z" || !got.FirstSeen.Equal(before.FirstSeen) {
		t.Errorf("persisted record mismatch: %+v", got)
	}
}

func TestUserStoreGetMissing(t *testing.T) {
	store, _ := NewUserStore(t.TempDir())
	_, ok, err := store.Get("google:nope")
	if err != nil || ok {
		t.Fatalf("Get missing = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
