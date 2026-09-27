package main

import (
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// Quiz mode: a 5-box Leitner system over eBird species codes, plus the
// session-composition and distractor-selection logic the quiz endpoint uses.
// Card identity is the species code and progress is global per account, not
// scoped to a hotspot (see userstore.go's card_progress).

// defaultQuizCount is the session size when the request doesn't ask for one.
const defaultQuizCount = 10

// Maximum session size accepted from a caller. `count=all` is the way to ask
// for "everything eligible", so an explicit huge number is still bounded to
// keep a single request from building an absurd response.
const maxQuizCount = 500

// leitnerIntervalDays maps a box to the number of days until that card is due
// again: box 1 due immediately (next session), then 1, 3, 7 and 21 days. Box 5
// is "mastered".
var leitnerIntervalDays = [6]int{0, 0, 1, 3, 7, 21}

// leitnerInterval is the spacing for a box. Boxes outside 0-5 are treated as
// due immediately rather than panicking on a corrupt row.
func leitnerInterval(box int) time.Duration {
	if box < 0 || box >= len(leitnerIntervalDays) {
		return 0
	}
	return time.Duration(leitnerIntervalDays[box]) * 24 * time.Hour
}

// CardAnswer is the result of answering one card, returned by the progress
// endpoint and by UserStore.AnswerCard.
type CardAnswer struct {
	Box           int   `json:"box"`
	DueAt         int64 `json:"dueAt"` // unix nanoseconds
	StarsEarned   int   `json:"starsEarned"`
	TotalStars    int   `json:"totalStars"`
	NewlyMastered bool  `json:"newlyMastered"`
}

// leitnerTransition computes the post-answer box and due time for a card that
// was in prevBox (0 = never answered) and has just been answered correctly or
// not. A correct answer advances one box (capped at 5) and earns a star; the
// first time a card reaches box 5 it earns three bonus stars and reports
// newlyMastered. A wrong answer resets to box 1 and is due immediately, with
// no stars.
func leitnerTransition(prevBox int, correct bool, now time.Time) (box int, dueAt time.Time, stars int, newlyMastered bool) {
	if !correct {
		return 1, now, 0, false
	}
	box = prevBox + 1
	if box > 5 {
		box = 5
	}
	if box < 1 {
		box = 1
	}
	stars = 1
	if box == 5 && prevBox < 5 {
		stars += 3
		newlyMastered = true
	}
	return box, now.Add(leitnerInterval(box)), stars, newlyMastered
}

// Question types offered by a quiz item.
const (
	questionTypeMultipleChoice = "multipleChoice"
	questionTypeRecall         = "recall"
)

// questionTypeForBox picks the question format from the card's box *before*
// the answer: boxes 0-1 get multiple choice, box 2+ gets free recall. This
// means reaching "known" (box 3) requires at least one unprompted recall.
func questionTypeForBox(box int) string {
	if box >= 2 {
		return questionTypeRecall
	}
	return questionTypeMultipleChoice
}

// Card states reported on a quiz item.
const (
	cardStateNew      = "new"
	cardStateReview   = "review"
	cardStateLearning = "learning"
)

// quizSpecies is one candidate species for a quiz session, carrying the
// taxonomy fields needed for grouping distractors. It is deliberately
// independent of the HTTP layer so session and distractor logic stay testable.
type quizSpecies struct {
	code       string
	comName    string
	sciName    string
	familyCode string
	order      string
}

// QuizChoice is one selectable name in a multiple-choice question.
type QuizChoice struct {
	SpeciesCode string `json:"speciesCode"`
	ComName     string `json:"comName"`
}

// QuizItem is one question in a session. Choices is present only for
// multipleChoice items.
type QuizItem struct {
	SpeciesCode  string       `json:"speciesCode"`
	ComName      string       `json:"comName"`
	SciName      string       `json:"sciName"`
	ImageURL     string       `json:"imageUrl"`
	ImageMissing bool         `json:"imageMissing,omitempty"`
	CardState    string       `json:"cardState"`
	QuestionType string       `json:"questionType"`
	Choices      []QuizChoice `json:"choices,omitempty"`
}

// QuizSession is the quiz endpoint's response body.
type QuizSession struct {
	Items []QuizItem `json:"items"`
}

// quizCandidate is a species selected for a session, tagged with the state
// that made it eligible and its box before this session's answer.
type quizCandidate struct {
	species      quizSpecies
	state        string
	box          int
	questionType string
}

// composeSession builds a session of at most count cards (count <= 0 means no
// cap) from pool, which is expected to be in popularity order. Eligibility is
// determined by progress and now, and cards are ordered: due reviews (most
// overdue first), then new cards (pool/popularity order), then learning cards
// that aren't due yet (soonest due first). Mastered cards that aren't due are
// excluded entirely.
func composeSession(pool []quizSpecies, progress map[string]CardProgress, now time.Time, count int) []quizCandidate {
	nowNS := now.UnixNano()

	var due, fresh, learning []quizCandidate
	for _, sp := range pool {
		p, ok := progress[sp.code]
		if !ok {
			fresh = append(fresh, quizCandidate{
				species:      sp,
				state:        cardStateNew,
				box:          0,
				questionType: questionTypeForBox(0),
			})
			continue
		}
		if p.DueAt <= nowNS {
			due = append(due, quizCandidate{
				species:      sp,
				state:        cardStateReview,
				box:          p.Box,
				questionType: questionTypeForBox(p.Box),
			})
			continue
		}
		if p.Box >= 3 {
			continue // mastered and not due: excluded from the session
		}
		learning = append(learning, quizCandidate{
			species:      sp,
			state:        cardStateLearning,
			box:          p.Box,
			questionType: questionTypeForBox(p.Box),
		})
	}

	// Most overdue first; ties fall back to popularity order via stable sort.
	sort.SliceStable(due, func(i, j int) bool {
		return progress[due[i].species.code].DueAt < progress[due[j].species.code].DueAt
	})
	// Soonest due first among learning cards.
	sort.SliceStable(learning, func(i, j int) bool {
		return progress[learning[i].species.code].DueAt < progress[learning[j].species.code].DueAt
	})

	out := make([]quizCandidate, 0, len(due)+len(fresh)+len(learning))
	out = append(out, due...)
	out = append(out, fresh...)
	out = append(out, learning...)
	if count > 0 && len(out) > count {
		out = out[:count]
	}
	return out
}

// chooseDistractors picks n distractor species for a multiple-choice question
// whose correct answer is correct. Sources are tried in order and the first
// with enough candidates wins: other species in the same eBird family within
// pool, then same order, then any other species in pool. Only when pool has
// fewer than four species (so none of those sources can supply n) does it
// fall back to fallback, the image cache's species generally. No species is
// repeated and the correct answer is never included.
func chooseDistractors(correct quizSpecies, pool, fallback []quizSpecies, n int, rng *rand.Rand) []quizSpecies {
	others := func(match func(quizSpecies) bool) []quizSpecies {
		var out []quizSpecies
		for _, s := range pool {
			if s.code == correct.code {
				continue
			}
			if match != nil && !match(s) {
				continue
			}
			out = append(out, s)
		}
		return out
	}
	sameFamily := others(func(s quizSpecies) bool {
		return correct.familyCode != "" && s.familyCode == correct.familyCode
	})
	sameOrder := others(func(s quizSpecies) bool {
		return correct.order != "" && s.order == correct.order
	})
	anyOther := others(nil)

	for _, src := range [][]quizSpecies{sameFamily, sameOrder, anyOther} {
		if len(src) >= n {
			return sampleSpecies(src, n, rng)
		}
	}

	// The hotspot doesn't have enough species on its own: take what it has,
	// then fill the rest from the image cache.
	chosen := append([]quizSpecies{}, anyOther...)
	need := n - len(chosen)
	if need > 0 {
		seen := map[string]bool{correct.code: true}
		for _, s := range chosen {
			seen[s.code] = true
		}
		extra := make([]quizSpecies, 0, len(fallback))
		for _, s := range fallback {
			if seen[s.code] {
				continue
			}
			seen[s.code] = true
			extra = append(extra, s)
		}
		if need > len(extra) {
			need = len(extra)
		}
		chosen = append(chosen, sampleSpecies(extra, need, rng)...)
	}
	if len(chosen) > n {
		chosen = chosen[:n]
	}
	return chosen
}

// sampleSpecies returns n distinct species drawn from src in random order.
func sampleSpecies(src []quizSpecies, n int, rng *rand.Rand) []quizSpecies {
	if n > len(src) {
		n = len(src)
	}
	perm := rng.Perm(len(src))[:n]
	out := make([]quizSpecies, 0, n)
	for _, i := range perm {
		out = append(out, src[i])
	}
	return out
}

// buildChoices assembles a multiple-choice question's options: the correct
// answer plus n distractors, shuffled.
func buildChoices(correct quizSpecies, pool, fallback []quizSpecies, n int, rng *rand.Rand) []QuizChoice {
	choices := []QuizChoice{{SpeciesCode: correct.code, ComName: correct.comName}}
	for _, d := range chooseDistractors(correct, pool, fallback, n, rng) {
		choices = append(choices, QuizChoice{SpeciesCode: d.code, ComName: d.comName})
	}
	rng.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
	return choices
}

// orderedQuizPool turns a hotspot's species codes + taxonomy into a pool of
// quiz candidates ordered most-likely-to-be-seen first. It reuses the GBIF
// nearby-report counts behind the species endpoint's "popularity" mode
// unconditionally; when that data is unavailable (no GBIF cache, an upstream
// error, or a test server) it falls back to the codes' taxonomic order.
func (s *Server) orderedQuizPool(locID string, codes []string, taxa map[string]Taxon) []quizSpecies {
	pool := make([]quizSpecies, 0, len(codes))
	for _, code := range codes {
		t, ok := taxa[code]
		if !ok {
			continue
		}
		pool = append(pool, quizSpecies{
			code:       code,
			comName:    t.ComName,
			sciName:    t.SciName,
			familyCode: t.FamilyCode,
			order:      t.Order,
		})
	}
	if s.ebirdCache == nil || s.gbif == nil {
		return pool
	}
	hotspot, err := s.ebirdCache.HotspotInfo(locID)
	if err != nil {
		log.Printf("quiz HotspotInfo(%s): %v", locID, err)
		return pool
	}
	counts, err := s.gbif.PopularityCounts(hotspot.Lat, hotspot.Lng)
	if err != nil {
		log.Printf("quiz PopularityCounts(%g, %g): %v", hotspot.Lat, hotspot.Lng, err)
		return pool
	}
	sort.SliceStable(pool, func(i, j int) bool {
		ci, iok := counts[pool[i].sciName]
		cj, jok := counts[pool[j].sciName]
		vi, vj := -1, -1
		if iok {
			vi = ci
		}
		if jok {
			vj = cj
		}
		if vi != vj {
			return vi > vj
		}
		return pool[i].comName < pool[j].comName
	})
	return pool
}

// cacheQuizFallback lists species from the image cache, for use as a
// last-resort distractor source. Returns nil when there is no image cache.
func (s *Server) cacheQuizFallback() []quizSpecies {
	if s.cache == nil {
		return nil
	}
	cached := s.cache.CachedSpecies()
	out := make([]quizSpecies, 0, len(cached))
	for _, c := range cached {
		out = append(out, quizSpecies{code: c.Code, comName: c.ComName, sciName: c.SciName})
	}
	return out
}

// handleHotspotQuiz builds and returns a quiz session for a hotspot. It reuses
// the species endpoint's species + taxonomy cache and (for new-card ordering)
// its GBIF popularity data.
func (s *Server) handleHotspotQuiz(w http.ResponseWriter, r *http.Request) {
	locID := r.PathValue("locId")
	if !validLocID(locID) {
		http.Error(w, "invalid locId", http.StatusBadRequest)
		return
	}
	lang, ok := s.resolveLang(w, r)
	if !ok {
		return
	}
	count, ok := parseQuizCount(r.URL.Query().Get("count"), w)
	if !ok {
		return
	}

	codes, taxa, err := s.ebirdCache.Species(locID, lang)
	if err != nil {
		log.Printf("quiz Species(%s, %s): %v", locID, lang, err)
		http.Error(w, "failed to fetch species from eBird", http.StatusBadGateway)
		return
	}

	pool := s.orderedQuizPool(locID, codes, taxa)
	progress, err := s.users.AllCardProgress(userIDFromContext(r))
	if err != nil {
		log.Printf("quiz AllCardProgress(%s): %v", userIDFromContext(r), err)
		http.Error(w, "failed to load progress", http.StatusInternalServerError)
		return
	}

	selected := composeSession(pool, progress, time.Now(), count)
	fallback := s.cacheQuizFallback()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	items := make([]QuizItem, 0, len(selected))
	for _, c := range selected {
		item := QuizItem{
			SpeciesCode:  c.species.code,
			ComName:      c.species.comName,
			SciName:      c.species.sciName,
			CardState:    c.state,
			QuestionType: c.questionType,
		}
		if c.species.sciName != "" && s.cache != nil {
			item.ImageURL = s.cache.ImageURLFor(c.species.sciName)
			if s.cache.IsKnownMissing(c.species.sciName) {
				item.ImageMissing = true
			} else {
				s.cache.EnsureFetchedAsync(c.species.sciName)
			}
		}
		if item.QuestionType == questionTypeMultipleChoice {
			item.Choices = buildChoices(c.species, pool, fallback, 3, rng)
		}
		items = append(items, item)
	}
	writeJSON(w, QuizSession{Items: items})
}

// parseQuizCount interprets the quiz endpoint's count query value: empty means
// the default, "all" means no cap, and anything else must be a positive
// integer. An invalid value writes a 400 and returns ok=false.
func parseQuizCount(v string, w http.ResponseWriter) (int, bool) {
	switch v {
	case "":
		return defaultQuizCount, true
	case "all":
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		http.Error(w, `count must be a positive integer or "all"`, http.StatusBadRequest)
		return 0, false
	}
	if n > maxQuizCount {
		n = maxQuizCount
	}
	return n, true
}
