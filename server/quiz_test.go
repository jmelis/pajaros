package main

import (
	"math/rand"
	"testing"
	"time"
)

func TestLeitnerTransition(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name            string
		prevBox         int
		correct         bool
		wantBox         int
		wantDue         time.Duration // offset from now
		wantStars       int
		wantNewlyMaster bool
	}{
		{"first correct", 0, true, 1, 0, 1, false},
		{"correct box1->2", 1, true, 2, 24 * time.Hour, 1, false},
		{"correct box2->3", 2, true, 3, 3 * 24 * time.Hour, 1, false},
		{"correct box3->4", 3, true, 4, 7 * 24 * time.Hour, 1, false},
		{"correct box4->5 masters", 4, true, 5, 21 * 24 * time.Hour, 4, true},
		{"correct at box5 no bonus", 5, true, 5, 21 * 24 * time.Hour, 1, false},
		{"incorrect resets to 1", 3, false, 1, 0, 0, false},
		{"incorrect at box5 resets", 5, false, 1, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			box, due, stars, newly := leitnerTransition(c.prevBox, c.correct, now)
			if box != c.wantBox {
				t.Errorf("box = %d, want %d", box, c.wantBox)
			}
			if want := now.Add(c.wantDue); !due.Equal(want) {
				t.Errorf("due = %v, want %v", due, want)
			}
			if stars != c.wantStars {
				t.Errorf("stars = %d, want %d", stars, c.wantStars)
			}
			if newly != c.wantNewlyMaster {
				t.Errorf("newlyMastered = %v, want %v", newly, c.wantNewlyMaster)
			}
		})
	}
}

func TestQuestionTypeForBox(t *testing.T) {
	cases := map[int]string{
		0: questionTypeMultipleChoice,
		1: questionTypeMultipleChoice,
		2: questionTypeRecall,
		3: questionTypeRecall,
		5: questionTypeRecall,
	}
	for box, want := range cases {
		if got := questionTypeForBox(box); got != want {
			t.Errorf("questionTypeForBox(%d) = %q, want %q", box, got, want)
		}
	}
}

func TestComposeSessionOrdersDueBeforeNewBeforeLearning(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pool := []quizSpecies{
		{code: "n1", comName: "New One"},
		{code: "l1", comName: "Learning One"},
		{code: "d1", comName: "Due One"},
		{code: "m1", comName: "Mastered One"},
		{code: "n2", comName: "New Two"},
	}
	progress := map[string]CardProgress{
		// Learning: box 2, not due for another day.
		"l1": {SpeciesCode: "l1", Box: 2, DueAt: now.Add(24 * time.Hour).UnixNano()},
		// Due: box 2, an hour overdue.
		"d1": {SpeciesCode: "d1", Box: 2, DueAt: now.Add(-time.Hour).UnixNano()},
		// Mastered and not due: excluded entirely.
		"m1": {SpeciesCode: "m1", Box: 5, DueAt: now.Add(24 * time.Hour).UnixNano()},
	}

	got := composeSession(pool, progress, now, 0)
	wantOrder := []string{"d1", "n1", "n2", "l1"}
	if len(got) != len(wantOrder) {
		t.Fatalf("session = %+v, want %d cards", got, len(wantOrder))
	}
	for i, code := range wantOrder {
		if got[i].species.code != code {
			t.Errorf("session[%d] = %s, want %s", i, got[i].species.code, code)
		}
	}
	if got[0].state != cardStateReview {
		t.Errorf("due card state = %q, want %q", got[0].state, cardStateReview)
	}
	if got[1].state != cardStateNew || got[2].state != cardStateNew {
		t.Errorf("new card states = %q, %q, want %q", got[1].state, got[2].state, cardStateNew)
	}
	if got[3].state != cardStateLearning {
		t.Errorf("learning card state = %q, want %q", got[3].state, cardStateLearning)
	}
	// Question type follows the box before the answer.
	if got[0].questionType != questionTypeRecall {
		t.Errorf("box-2 due card questionType = %q, want recall", got[0].questionType)
	}
	if got[1].questionType != questionTypeMultipleChoice {
		t.Errorf("new card questionType = %q, want multipleChoice", got[1].questionType)
	}
}

func TestComposeSessionOrdersDueByMostOverdue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pool := []quizSpecies{{code: "a"}, {code: "b"}, {code: "c"}}
	progress := map[string]CardProgress{
		"a": {Box: 1, DueAt: now.Add(-time.Hour).UnixNano()},
		"b": {Box: 1, DueAt: now.Add(-48 * time.Hour).UnixNano()},
		"c": {Box: 1, DueAt: now.Add(-time.Minute).UnixNano()},
	}
	got := composeSession(pool, progress, now, 0)
	want := []string{"b", "a", "c"} // most overdue first
	for i, code := range want {
		if got[i].species.code != code {
			t.Errorf("session[%d] = %s, want %s", i, got[i].species.code, code)
		}
	}
}

func TestComposeSessionCapsAtCountAndHandlesTooFew(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	pool := []quizSpecies{{code: "a"}, {code: "b"}, {code: "c"}, {code: "d"}, {code: "e"}}

	if got := composeSession(pool, nil, now, 3); len(got) != 3 {
		t.Errorf("cap at 3 returned %d cards, want 3", len(got))
	}
	if got := composeSession(pool[:2], nil, now, 10); len(got) != 2 {
		t.Errorf("pool smaller than count returned %d cards, want 2", len(got))
	}
	if got := composeSession(nil, nil, now, 10); len(got) != 0 {
		t.Errorf("empty pool returned %d cards, want 0", len(got))
	}
}

func TestComposeSessionLearningExcludedWhenMasteredIsDue(t *testing.T) {
	// A box>=3 card that is due is a review, not excluded; a box>=3 card that
	// is not due is excluded.
	now := time.Unix(1_700_000_000, 0)
	pool := []quizSpecies{{code: "masteredDue"}, {code: "masteredLater"}}
	progress := map[string]CardProgress{
		"masteredDue":   {Box: 5, DueAt: now.Add(-time.Hour).UnixNano()},
		"masteredLater": {Box: 5, DueAt: now.Add(time.Hour).UnixNano()},
	}
	got := composeSession(pool, progress, now, 0)
	if len(got) != 1 || got[0].species.code != "masteredDue" {
		t.Fatalf("session = %+v, want only masteredDue", got)
	}
	if got[0].state != cardStateReview || got[0].questionType != questionTypeRecall {
		t.Errorf("mastered-due card = %+v, want review/recall", got[0])
	}
}

func quizSp(code, comName, family, order string) quizSpecies {
	return quizSpecies{code: code, comName: comName, familyCode: family, order: order}
}

func TestChooseDistractorsPrefersSameFamily(t *testing.T) {
	correct := quizSp("correct", "Correct", "famA", "ordX")
	pool := []quizSpecies{
		correct,
		quizSp("a1", "A1", "famA", "ordX"),
		quizSp("a2", "A2", "famA", "ordX"),
		quizSp("a3", "A3", "famA", "ordX"),
		quizSp("b1", "B1", "famB", "ordY"),
	}
	got := chooseDistractors(correct, pool, nil, 3, rand.New(rand.NewSource(1)))
	if len(got) != 3 {
		t.Fatalf("got %d distractors, want 3", len(got))
	}
	for _, d := range got {
		if d.familyCode != "famA" {
			t.Errorf("distractor %s family = %s, want famA (family source)", d.code, d.familyCode)
		}
		if d.code == correct.code {
			t.Error("distractor included the correct answer")
		}
	}
}

func TestChooseDistractorsFallsBackToOrder(t *testing.T) {
	correct := quizSp("correct", "Correct", "famA", "ordX")
	pool := []quizSpecies{
		correct,
		quizSp("a1", "A1", "famA", "ordX"), // only one same-family candidate
		quizSp("b1", "B1", "famB", "ordX"),
		quizSp("b2", "B2", "famB", "ordX"),
		quizSp("b3", "B3", "famB", "ordX"),
	}
	got := chooseDistractors(correct, pool, nil, 3, rand.New(rand.NewSource(2)))
	if len(got) != 3 {
		t.Fatalf("got %d distractors, want 3", len(got))
	}
	sameFamily := 0
	for _, d := range got {
		if d.order != "ordX" {
			t.Errorf("distractor %s order = %s, want ordX (order source)", d.code, d.order)
		}
		if d.familyCode == "famA" {
			sameFamily++
		}
	}
	if sameFamily == len(got) {
		t.Error("all distractors came from the family source; expected the order source")
	}
}

func TestChooseDistractorsFallsBackToWholeHotspot(t *testing.T) {
	correct := quizSp("correct", "Correct", "famA", "ordX")
	pool := []quizSpecies{
		correct,
		quizSp("b1", "B1", "famB", "ordY"),
		quizSp("c1", "C1", "famC", "ordZ"),
		quizSp("d1", "D1", "famD", "ordW"),
		quizSp("e1", "E1", "famE", "ordV"),
	}
	got := chooseDistractors(correct, pool, nil, 3, rand.New(rand.NewSource(3)))
	if len(got) != 3 {
		t.Fatalf("got %d distractors, want 3", len(got))
	}
	for _, d := range got {
		if d.code == correct.code {
			t.Error("distractor included the correct answer")
		}
	}
}

func TestChooseDistractorsUsesCacheWhenHotspotTooSmall(t *testing.T) {
	correct := quizSp("correct", "Correct", "famA", "ordX")
	pool := []quizSpecies{
		correct,
		quizSp("only", "Only Other", "famB", "ordY"), // hotspot has just 2 species
	}
	fallback := []quizSpecies{
		quizSp("f1", "Fallback 1", "", ""),
		quizSp("f2", "Fallback 2", "", ""),
		quizSp("f3", "Fallback 3", "", ""),
		quizSp("f4", "Fallback 4", "", ""),
		quizSp("correct", "Correct", "", ""), // must not leak in
	}
	got := chooseDistractors(correct, pool, fallback, 3, rand.New(rand.NewSource(4)))
	if len(got) != 3 {
		t.Fatalf("got %d distractors, want 3", len(got))
	}
	seen := map[string]bool{}
	fromPool, fromFallback := 0, 0
	for _, d := range got {
		if d.code == correct.code {
			t.Error("distractor included the correct answer")
		}
		if seen[d.code] {
			t.Errorf("distractor %s repeated", d.code)
		}
		seen[d.code] = true
		if d.code == "only" {
			fromPool++
		} else {
			fromFallback++
		}
	}
	if fromPool != 1 || fromFallback != 2 {
		t.Errorf("distractors: %d from hotspot, %d from cache; want 1 and 2", fromPool, fromFallback)
	}
}

// countingSpeciesLister records how often the image-cache fallback pool is
// consulted, so a test can prove the directory scan is skipped when it can't
// matter.
type countingSpeciesLister struct {
	calls   int
	species []CacheSpecies
}

func (c *countingSpeciesLister) CachedSpecies(lang string) []CacheSpecies {
	c.calls++
	return c.species
}

func TestQuizFallbackPoolSkipsCacheWhenHotspotIsBigEnough(t *testing.T) {
	lister := &countingSpeciesLister{species: []CacheSpecies{{Code: "cached", SciName: "Cachedus birdus", ComName: "Cached Bird"}}}
	pool := []quizSpecies{
		quizSp("a", "A", "", ""),
		quizSp("b", "B", "", ""),
		quizSp("c", "C", "", ""),
		quizSp("d", "D", "", ""),
	}

	// Four candidates are enough to fill a multiple-choice question, so the
	// image cache must not be scanned at all.
	if got := quizFallbackPool(lister, pool, "en"); got != nil {
		t.Errorf("quizFallbackPool(4-species pool) = %+v, want nil", got)
	}
	if lister.calls != 0 {
		t.Errorf("image cache scanned %d times for a 4-species pool, want 0", lister.calls)
	}

	// Three candidates are not, so the cache pool is built once (with names).
	got := quizFallbackPool(lister, pool[:3], "en")
	if lister.calls != 1 {
		t.Errorf("image cache scanned %d times for a 3-species pool, want 1", lister.calls)
	}
	if len(got) != 1 || got[0].comName != "Cached Bird" || got[0].code != "cached" {
		t.Errorf("quizFallbackPool(3-species pool) = %+v, want the cached species", got)
	}
}

func TestBuildChoicesIncludesCorrectOnceAndShuffles(t *testing.T) {
	correct := quizSp("correct", "Correct", "famA", "ordX")
	pool := []quizSpecies{
		correct,
		quizSp("a1", "A1", "famA", "ordX"),
		quizSp("a2", "A2", "famA", "ordX"),
		quizSp("a3", "A3", "famA", "ordX"),
	}
	choices := buildChoices(correct, pool, nil, 3, rand.New(rand.NewSource(5)))
	if len(choices) != 4 {
		t.Fatalf("choices = %d, want 4", len(choices))
	}
	seen := map[string]int{}
	for _, c := range choices {
		seen[c.SpeciesCode]++
	}
	if seen["correct"] != 1 {
		t.Errorf("correct answer appears %d times, want 1", seen["correct"])
	}
	if len(seen) != 4 {
		t.Errorf("choices are not unique: %v", choices)
	}
}

func TestAnswerCardLeitnerStore(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Upsert("google", "quiz", "", ""); err != nil {
		t.Fatal(err)
	}
	const id = "google:quiz"
	now := time.Unix(1_700_000_000, 0)

	wantBoxes := []int{1, 2, 3, 4, 5}
	for i, wantBox := range wantBoxes {
		answer, err := store.AnswerCard(id, "vermfly", true, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("AnswerCard(%d): %v", i, err)
		}
		if answer.Box != wantBox {
			t.Errorf("answer %d box = %d, want %d", i, answer.Box, wantBox)
		}
		wantNewly := wantBox == 5
		if answer.NewlyMastered != wantNewly {
			t.Errorf("answer %d newlyMastered = %v, want %v", i, answer.NewlyMastered, wantNewly)
		}
	}

	// Stars: five single-star answers plus the 3-star mastery bonus on the
	// last one = 8; the repeat answer adds one more.
	if answer, err := store.AnswerCard(id, "vermfly", true, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	} else if answer.StarsEarned != 1 || answer.TotalStars != 9 {
		t.Errorf("box-5 repeat: earned %d total %d, want 1/9", answer.StarsEarned, answer.TotalStars)
	}

	progress, err := store.AllCardProgress(id)
	if err != nil {
		t.Fatal(err)
	}
	if p := progress["vermfly"]; p.Box != 5 || p.SeenCount != 6 || p.CorrectCount != 6 {
		t.Errorf("stored progress = %+v, want box5/seen6/correct6", p)
	}

	stats, err := store.ProgressStats(id, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalStars != 9 || stats.SpeciesMastered != 1 || stats.SpeciesLearning != 0 {
		t.Errorf("stats = %+v, want 9 stars / 1 mastered / 0 learning", stats)
	}
	// The box-5 card's due date is 21 days out, so at this point nothing is
	// due for review.
	if stats.DueForReview != 0 {
		t.Errorf("DueForReview = %d, want 0 before the box-5 due date", stats.DueForReview)
	}

	// A wrong answer resets to box 1, due immediately, with no stars.
	answer, err := store.AnswerCard(id, "vermfly", false, now.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if answer.Box != 1 || answer.DueAt != now.Add(20*time.Minute).UnixNano() || answer.StarsEarned != 0 {
		t.Errorf("wrong answer = %+v, want box1/due-now/no stars", answer)
	}
	if answer.TotalStars != 9 {
		t.Errorf("total stars after wrong answer = %d, want unchanged 9", answer.TotalStars)
	}

	stats, _ = store.ProgressStats(id, now.Add(30*time.Minute))
	if stats.SpeciesMastered != 0 || stats.SpeciesLearning != 1 {
		t.Errorf("stats after reset = %+v, want 0 mastered / 1 learning", stats)
	}
	// The wrong answer reset the card to box 1, due immediately.
	if stats.DueForReview != 1 {
		t.Errorf("DueForReview after reset = %d, want 1", stats.DueForReview)
	}
}
