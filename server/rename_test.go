package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenameFavorite(t *testing.T) {
	app, _, store, auth := newPrefsTestApp(t)
	if _, err := store.Upsert("google", "ren", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:ren"
	const key = "c50.850,4.350,5.0"
	put := func(path, payload string) int {
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, path, body(payload)))
		return rr.Code
	}

	if code := put("/api/me/favorites/"+key+"/name", `{"name":"Home"}`); code != http.StatusNotFound {
		t.Fatalf("rename of an unsaved area = %d, want 404", code)
	}
	if code := put("/api/me/favorites/"+key, ""); code != http.StatusNoContent {
		t.Fatalf("add = %d", code)
	}
	if code := put("/api/me/favorites/"+key+"/name", `{"name":"  My   garden "}`); code != http.StatusNoContent {
		t.Fatalf("rename = %d, want 204", code)
	}
	if favs := getFavorites(t, app, auth, id); len(favs) != 1 || favs[0].CustomName != "My garden" {
		t.Fatalf("after rename: %+v", favs)
	}

	put("/api/me/favorites/"+key, "")
	if favs := getFavorites(t, app, auth, id); favs[0].CustomName != "My garden" {
		t.Fatalf("saving again lost the label: %+v", favs)
	}

	bad := []string{
		`{"name":"` + strings.Repeat("x", maxFavoriteNameRunes+1) + `"}`,
		`{"name":"ab"}`,
		`nope`,
	}
	for _, b := range bad {
		if code := put("/api/me/favorites/"+key+"/name", b); code != http.StatusBadRequest {
			t.Errorf("rename with %.20q = %d, want 400", b, code)
		}
	}

	if code := put("/api/me/favorites/"+key+"/name", `{"name":""}`); code != http.StatusNoContent {
		t.Fatalf("clear = %d", code)
	}
	if favs := getFavorites(t, app, auth, id); favs[0].CustomName != "" {
		t.Fatalf("after clear: %+v", favs)
	}
}
