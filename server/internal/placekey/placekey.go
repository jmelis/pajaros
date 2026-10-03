// Package placekey normalises place names into search keys. The place
// builder (cmd/gensnapshot) and the server's search must agree exactly, so both
// call this.
package placekey

import (
	"strings"
	"unicode"
)

// diacriticFold maps a common accented Latin letter to its plain-ASCII
// base, covering the everyday "strip the accent" reading of a name — as
// opposed to GeoNames' own asciiname column, which transliterates by each
// language's own convention (ü -> "ue" in German place names, not "u").
// Deliberately not exhaustive: anything outside this table makes
// Fold bail rather than guess.
var diacriticFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a', 'ă': 'a', 'ą': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ĕ': 'e', 'ė': 'e', 'ę': 'e', 'ě': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i', 'ĭ': 'i', 'į': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'ō': 'o', 'ŏ': 'o', 'ő': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u', 'ŭ': 'u', 'ů': 'u', 'ű': 'u', 'ų': 'u',
	'ñ': 'n', 'ń': 'n', 'ņ': 'n', 'ň': 'n',
	'ç': 'c', 'ć': 'c', 'ĉ': 'c', 'č': 'c',
	'ý': 'y', 'ÿ': 'y',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
	'š': 's', 'ś': 's', 'ş': 's',
	'ğ': 'g', 'ĝ': 'g',
	'ł': 'l', 'ĺ': 'l', 'ľ': 'l',
	'ř': 'r', 'ŕ': 'r',
	'ť': 't', 'ţ': 't',
	'đ': 'd', 'ď': 'd',
	'ß': 's',
}

// Fold returns s with every accented letter replaced by its
// plain-ASCII base, and ok = true only if the result is now fully ASCII —
// a name with any character outside diacriticFold's table (a
// different script entirely, or an accent this table doesn't know) isn't
// coverable, so the caller skips indexing a folded alias for it rather
// than index a string still full of non-Latin characters.
func Fold(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		lower := unicode.ToLower(r)
		if folded, ok := diacriticFold[lower]; ok {
			b.WriteRune(folded)
			continue
		}
		if r > unicode.MaxASCII {
			return "", false
		}
		b.WriteRune(lower)
	}
	return b.String(), true
}

// Normalize is the key form of a name: lowercased and trimmed, with accents
// stripped when the whole name can be folded to ASCII (names in other scripts
// are only lowercased).
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	if f, ok := Fold(s); ok {
		return f
	}
	return strings.ToLower(s)
}
