package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newQuizTestApp wires the quiz, progress, and preference routes behind the
// same auth gate as main.go, with a fresh database and file cache, and a
// fake species source so tests don't need real hotspot/bbolt/taxonomy data.
// Popularity order stays untested unless a test calls fakeSpeciesSource's
// setCounts explicitly (see TestQuizNewCardsFollowPopularityOrder).
func newQuizTestApp(t *testing.T) (http.Handler, *Server, *UserStore, *Auth) {
	t.Helper()

	store, _ := newTestStore(t)
	cacheDir := t.TempDir()
	imgCache, err := NewImageCache(cacheDir)
	if err != nil {
		t.Fatalf("NewImageCache: %v", err)
	}
	srv := &Server{users: store, cache: imgCache, species: newFakeSpeciesSource()}

	signer, err := newCookieSigner("test-secret")
	if err != nil {
		t.Fatalf("newCookieSigner: %v", err)
	}
	auth := &Auth{signer: signer, users: store}

	app := http.NewServeMux()
	app.HandleFunc("GET /api/me", srv.handleGetProfile)
	app.HandleFunc("GET /api/me/language", srv.handleGetLanguage)
	app.HandleFunc("PUT /api/me/language", srv.handleSetLanguage)
	app.HandleFunc("GET /api/me/secondary-language", srv.handleGetSecondaryLanguage)
	app.HandleFunc("PUT /api/me/secondary-language", srv.handleSetSecondaryLanguage)
	app.HandleFunc("GET /api/me/progress", srv.handleGetProgress)
	app.HandleFunc("POST /api/me/progress/{speciesCode}", srv.handleSubmitAnswer)
	app.HandleFunc("DELETE /api/me/progress", srv.handleResetProgress)
	app.HandleFunc("GET /api/me/mastered", srv.handleGetMastered)
	app.HandleFunc("GET /api/hotspots/{locId}/species", srv.handleHotspotSpecies)
	app.HandleFunc("GET /api/hotspots/{locId}/quiz", srv.handleHotspotQuiz)

	root := http.NewServeMux()
	root.Handle("/", auth.require(app))
	return root, srv, store, auth
}

func quizTaxon(code, com, family, order string) Taxon {
	// Empty SciName keeps the quiz handler from kicking off an image fetch.
	return Taxon{SpeciesCode: code, ComName: com, FamilyCode: family, Order: order}
}

func seedHotspotSpecies(t *testing.T, srv *Server, locID, lang string, codes []string, taxa map[string]Taxon) {
	t.Helper()
	srv.species.(*fakeSpeciesSource).set(locID, lang, codes, taxa)
}

func getProgress(t *testing.T, app http.Handler, auth *Auth, id string) ProgressStats {
	t.Helper()
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me/progress", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/me/progress = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var stats ProgressStats
	if err := json.Unmarshal(rr.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	return stats
}

func getQuiz(t *testing.T, app http.Handler, auth *Auth, id, target string) QuizSession {
	t.Helper()
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, target, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d (%s), want 200", target, rr.Code, rr.Body.String())
	}
	var session QuizSession
	if err := json.Unmarshal(rr.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode quiz session: %v", err)
	}
	return session
}

func submitAnswer(t *testing.T, app http.Handler, auth *Auth, id, speciesCode string, correct bool) CardAnswer {
	t.Helper()
	bodyJSON := `{"correct":false}`
	if correct {
		bodyJSON = `{"correct":true}`
	}
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPost, "/api/me/progress/"+speciesCode, body(bodyJSON)))
	if rr.Code != http.StatusOK {
		t.Fatalf("POST progress/%s = %d (%s), want 200", speciesCode, rr.Code, rr.Body.String())
	}
	var answer CardAnswer
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	return answer
}

func TestQuizAndProgressRoutesRequireAuth(t *testing.T) {
	app, _, _, _ := newQuizTestApp(t)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/hotspots/41.5,-70.5/quiz"},
		{http.MethodGet, "/api/me/progress"},
		{http.MethodPost, "/api/me/progress/vermfly"},
		{http.MethodDelete, "/api/me/progress"},
		{http.MethodGet, "/api/me/secondary-language"},
		{http.MethodPut, "/api/me/secondary-language"},
		{http.MethodGet, "/api/me/mastered"},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rr.Code)
		}
	}
}

func TestQuizFreshHotspotAllNewMultipleChoice(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "fresh", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:fresh"

	codes := []string{"sp1", "sp2", "sp3", "sp4", "sp5"}
	taxa := map[string]Taxon{}
	for _, code := range codes {
		taxa[code] = quizTaxon(code, "Common "+code, "fam", "ord")
	}
	seedHotspotSpecies(t, srv, "41.5,-70.5", defaultLang, codes, taxa)

	session := getQuiz(t, app, auth, id, "/api/hotspots/41.5,-70.5/quiz?count=5")
	if len(session.Items) != 5 {
		t.Fatalf("quiz returned %d items, want 5", len(session.Items))
	}
	for _, item := range session.Items {
		if item.CardState != cardStateNew {
			t.Errorf("item %s cardState = %q, want new", item.SpeciesCode, item.CardState)
		}
		if item.QuestionType != questionTypeMultipleChoice {
			t.Errorf("item %s questionType = %q, want multipleChoice", item.SpeciesCode, item.QuestionType)
		}
		if len(item.Choices) != 4 {
			t.Fatalf("item %s has %d choices, want 4", item.SpeciesCode, len(item.Choices))
		}
		seen := map[string]bool{}
		foundCorrect := false
		for _, c := range item.Choices {
			if seen[c.SpeciesCode] {
				t.Errorf("item %s choices repeat %s", item.SpeciesCode, c.SpeciesCode)
			}
			seen[c.SpeciesCode] = true
			if c.SpeciesCode == item.SpeciesCode {
				foundCorrect = true
			}
		}
		if !foundCorrect {
			t.Errorf("item %s choices omit the correct answer", item.SpeciesCode)
		}
	}
}

func TestQuizQuestionTypeAndBoxTransitions(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "boxes", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:boxes"

	codes := []string{"sp1", "sp2", "sp3", "sp4", "sp5", "sp6"}
	taxa := map[string]Taxon{}
	for _, code := range codes {
		taxa[code] = quizTaxon(code, "Common "+code, "fam", "ord")
	}
	seedHotspotSpecies(t, srv, "41.5,-70.5", defaultLang, codes, taxa)

	// Three correct answers walk the card through boxes 1 -> 2 -> 3.
	for wantBox := 1; wantBox <= 3; wantBox++ {
		answer := submitAnswer(t, app, auth, id, "sp1", true)
		if answer.Box != wantBox {
			t.Fatalf("answer %d box = %d, want %d", wantBox, answer.Box, wantBox)
		}
	}

	// Box 3 is due three days out, so it is mastered-not-due and excluded from
	// the next session ("if still eligible").
	session := getQuiz(t, app, auth, id, "/api/hotspots/41.5,-70.5/quiz?count=all")
	for _, item := range session.Items {
		if item.SpeciesCode == "sp1" {
			t.Errorf("box-3 sp1 should not be in the session, got state %q", item.CardState)
		}
	}

	// A second species answered twice sits in box 2 (learning, not due), which
	// is included when the session has room and must be a recall question.
	submitAnswer(t, app, auth, id, "sp2", true)
	submitAnswer(t, app, auth, id, "sp2", true)
	session = getQuiz(t, app, auth, id, "/api/hotspots/41.5,-70.5/quiz?count=all")
	var found *QuizItem
	for i := range session.Items {
		if session.Items[i].SpeciesCode == "sp2" {
			found = &session.Items[i]
		}
	}
	if found == nil {
		t.Fatal("box-2 sp2 (learning, not due) should be included with count=all")
	}
	if found.QuestionType != questionTypeRecall {
		t.Errorf("box-2 sp2 questionType = %q, want recall", found.QuestionType)
	}
	if found.CardState != cardStateLearning {
		t.Errorf("box-2 sp2 cardState = %q, want learning", found.CardState)
	}
	if len(found.Choices) != 0 {
		t.Errorf("recall item should carry no choices, got %d", len(found.Choices))
	}
}

func TestQuizCrossHotspotDoesNotReintroduceKnownSpeciesAsNew(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "cross", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:cross"

	codes := []string{"sp1", "sp2", "sp3", "sp4", "sp5"}
	taxa := map[string]Taxon{}
	for _, code := range codes {
		taxa[code] = quizTaxon(code, "Common "+code, "fam", "ord")
	}
	// Both hotspots share sp1.
	seedHotspotSpecies(t, srv, "41.5,-70.5", defaultLang, codes, taxa)
	seedHotspotSpecies(t, srv, "42.5,-71.5", defaultLang, codes, taxa)

	// Take sp1 to "known" (box 3) at 41.5,-70.5.
	for i := 0; i < 3; i++ {
		submitAnswer(t, app, auth, id, "sp1", true)
	}
	session := getQuiz(t, app, auth, id, "/api/hotspots/42.5,-71.5/quiz?count=all")
	for _, item := range session.Items {
		if item.SpeciesCode == "sp1" && item.CardState == cardStateNew {
			t.Error("species learned at 41.5,-70.5 was reintroduced as new at 42.5,-71.5")
		}
	}

	// A card with a row but not yet mastered (box 1, due now) still must not
	// read as new: it comes back as a review.
	submitAnswer(t, app, auth, id, "sp2", true) // box 1, due immediately
	session = getQuiz(t, app, auth, id, "/api/hotspots/42.5,-71.5/quiz?count=all")
	var found *QuizItem
	for i := range session.Items {
		if session.Items[i].SpeciesCode == "sp2" {
			found = &session.Items[i]
		}
	}
	if found == nil {
		t.Fatal("due box-1 sp2 should appear as a review")
	}
	if found.CardState != cardStateReview {
		t.Errorf("box-1 sp2 cardState = %q, want review", found.CardState)
	}
}

func TestResetProgressClearsStatsAndQuiz(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "reset", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:reset"

	codes := []string{"sp1", "sp2", "sp3", "sp4", "sp5"}
	taxa := map[string]Taxon{}
	for _, code := range codes {
		taxa[code] = quizTaxon(code, "Common "+code, "fam", "ord")
	}
	seedHotspotSpecies(t, srv, "41.5,-70.5", defaultLang, codes, taxa)

	submitAnswer(t, app, auth, id, "sp1", true)
	submitAnswer(t, app, auth, id, "sp1", true)
	if stats := getProgress(t, app, auth, id); stats.SpeciesLearning == 0 {
		t.Fatalf("stats before reset = %+v, want non-zero learning", stats)
	}

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodDelete, "/api/me/progress", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/me/progress = %d, want 204", rr.Code)
	}

	if stats := getProgress(t, app, auth, id); stats.SpeciesMastered != 0 || stats.SpeciesLearning != 0 {
		t.Fatalf("stats after reset = %+v, want all zero", stats)
	}

	for _, item := range getQuiz(t, app, auth, id, "/api/hotspots/41.5,-70.5/quiz?count=all").Items {
		if item.CardState != cardStateNew {
			t.Errorf("after reset, item %s cardState = %q, want new", item.SpeciesCode, item.CardState)
		}
	}
}

func TestProfileReflectsSecondaryLanguage(t *testing.T) {
	app, _, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "prefs", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:prefs"

	readProfile := func() Profile {
		t.Helper()
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /api/me = %d", rr.Code)
		}
		var p Profile
		if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode profile: %v", err)
		}
		return p
	}

	p := readProfile()
	if p.SecondaryLanguage != "" || p.SpeciesMastered != 0 {
		t.Errorf("new-account profile = %+v, want empty secondary/0", p)
	}

	// Invalid values are rejected.
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, "/api/me/secondary-language", body(`{"language":"xx"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid secondary language = %d, want 400", rr.Code)
	}

	put := func(path, payload string) {
		t.Helper()
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPut, path, body(payload)))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("PUT %s = %d (%s), want 204", path, rr.Code, rr.Body.String())
		}
	}

	put("/api/me/secondary-language", `{"language":"fr"}`)
	p = readProfile()
	if p.SecondaryLanguage != "fr" {
		t.Errorf("profile after set = %+v, want fr", p)
	}

	// The dedicated GET endpoint agrees.
	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me/secondary-language", nil))
	var lang struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &lang); err != nil || lang.Language != "fr" {
		t.Errorf("GET secondary-language = %v (err %v), want fr", rr.Body.String(), err)
	}

	// Empty clears the secondary language.
	put("/api/me/secondary-language", `{"language":""}`)
	if p = readProfile(); p.SecondaryLanguage != "" {
		t.Errorf("secondary language after clear = %q, want empty", p.SecondaryLanguage)
	}
}

func TestQuizNewCardsFollowPopularityOrder(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "pop", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:pop"
	const locID = "50.0,4.0"

	codes := []string{"sp1", "sp2", "sp3", "sp4", "sp5"}
	taxa := map[string]Taxon{}
	for _, code := range codes {
		sci := "Sci " + code
		taxa[code] = Taxon{SpeciesCode: code, ComName: "Common " + code, SciName: sci, FamilyCode: "fam", Order: "ord"}
		// Pre-place an image so the handler's async fetch is a no-op.
		if err := os.WriteFile(filepath.Join(srv.cache.dir, slugify(sci)+".jpg"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seedHotspotSpecies(t, srv, locID, defaultLang, codes, taxa)

	// sp3 is by far the most reported, sp1 next, the rest absent.
	srv.species.(*fakeSpeciesSource).setCounts(locID, map[string]int{"Sci sp3": 100, "Sci sp1": 5})

	session := getQuiz(t, app, auth, id, "/api/hotspots/"+locID+"/quiz?count=all")
	if len(session.Items) < 2 || session.Items[0].SpeciesCode != "sp3" || session.Items[1].SpeciesCode != "sp1" {
		t.Fatalf("quiz order = %v, want sp3 first then sp1 (popularity)", speciesCodes(session.Items))
	}
}

func speciesCodes(items []QuizItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.SpeciesCode)
	}
	return out
}

func TestCachedSpeciesReadsMetadata(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Current entries carry both the scientific name and per-locale common
	// names, so the fallback pool can label options like every other source.
	meta, _ := json.Marshal(ImageInfo{
		SciName: "Turdus merula",
		Names:   map[string]string{"en": "Common Blackbird", "es": "Mirlo común"},
		Title:   "Turdus merula 1.jpg",
	})
	if err := os.WriteFile(filepath.Join(dir, slugify("Turdus merula")+".json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	// A legacy entry without a recorded common name is skipped rather than
	// falling back to a raw slug or scientific name.
	if err := os.WriteFile(filepath.Join(dir, "parus-major.json"), []byte(`{"Title":"Parus major.jpg"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-metadata files are ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cache.CachedSpecies("es")
	if len(got) != 1 {
		t.Fatalf("CachedSpecies(es) = %+v, want 1 common-named entry", got)
	}
	c := got[0]
	if c.Code != "turdus-merula" || c.SciName != "Turdus merula" || c.ComName != "Mirlo común" {
		t.Errorf("CachedSpecies(es) entry = %+v, want turdus-merula / Turdus merula / Mirlo común", c)
	}

	// A locale with no recorded name falls back to English, never to a slug.
	got = cache.CachedSpecies("fr")
	if len(got) != 1 || got[0].ComName != "Common Blackbird" {
		t.Fatalf("CachedSpecies(fr) = %+v, want the English common name", got)
	}
}

func TestSubmitAnswerValidatesInput(t *testing.T) {
	app, _, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "valid", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:valid"

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPost, "/api/me/progress/bad!code", body(`{"correct":true}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid speciesCode = %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodPost, "/api/me/progress/vermfly", body(`not json`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid body = %d, want 400", rr.Code)
	}
}

func TestQuizRejectsBadCount(t *testing.T) {
	app, srv, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "count", "", ""); err != nil {
		t.Fatal(err)
	}
	seedHotspotSpecies(t, srv, "41.5,-70.5", defaultLang, []string{"sp1"}, map[string]Taxon{"sp1": quizTaxon("sp1", "One", "fam", "ord")})

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, "google:count", http.MethodGet, "/api/hotspots/41.5,-70.5/quiz?count=0", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("count=0 = %d, want 400", rr.Code)
	}
}

func TestProgressSummaryIncludesDueForReview(t *testing.T) {
	app, _, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "due", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:due"

	// A wrong answer resets the card to box 1, due immediately.
	submitAnswer(t, app, auth, id, "sp1", false)

	stats := getProgress(t, app, auth, id)
	if stats.SpeciesLearning != 1 || stats.SpeciesMastered != 0 {
		t.Errorf("stats = %+v, want 0 mastered / 1 learning", stats)
	}
	if stats.DueForReview != 1 {
		t.Errorf("DueForReview = %d, want 1 (box 1 is due now)", stats.DueForReview)
	}
}

func TestMasteredEndpointNamesBirdsAndFallsBack(t *testing.T) {
	app, _, store, auth := newQuizTestApp(t)
	if _, err := store.Upsert("google", "mastered", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:mastered"

	// Record taxonomy for two species: one with a Spanish name, one only in
	// English. A third species is mastered with no recorded taxonomy at all.
	if err := store.RecordTaxa(map[string]Taxon{
		"sp1": {SpeciesCode: "sp1", SciName: "Sci one", ComName: "Uno"},
	}, "es"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTaxa(map[string]Taxon{
		"sp2": {SpeciesCode: "sp2", SciName: "Sci two", ComName: "Two"},
	}, "en"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		submitAnswer(t, app, auth, id, "sp1", true)
		submitAnswer(t, app, auth, id, "sp2", true)
		submitAnswer(t, app, auth, id, "sp3", true)
	}

	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, authedRequest(t, auth, id, http.MethodGet, "/api/me/mastered?lang=es", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/me/mastered = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var birds []MasteredSpecies
	if err := json.Unmarshal(rr.Body.Bytes(), &birds); err != nil {
		t.Fatalf("decode mastered: %v", err)
	}
	if len(birds) != 3 {
		t.Fatalf("mastered = %+v, want 3 birds", birds)
	}
	byCode := map[string]MasteredSpecies{}
	for _, b := range birds {
		byCode[b.SpeciesCode] = b
	}

	if got := byCode["sp1"]; got.ComName != "Uno" || got.SciName != "Sci one" {
		t.Errorf("sp1 = %+v, want Uno / Sci one", got)
	}
	if got := byCode["sp1"]; got.Box != 5 || got.SeenCount != 5 || got.CorrectCount != 5 {
		t.Errorf("sp1 stats = %+v, want box5/seen5/correct5", got)
	}
	// sp2 has no Spanish name: it falls back to English rather than vanishing.
	if got := byCode["sp2"]; got.ComName != "Two" {
		t.Errorf("sp2 ComName = %q, want the English fallback Two", got.ComName)
	}
	// sp3 has no taxonomy at all: it still appears, named by its code.
	if got := byCode["sp3"]; got.ComName != "sp3" {
		t.Errorf("sp3 ComName = %q, want the species code fallback", got.ComName)
	}
}
