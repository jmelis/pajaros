package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestTranslationsHaveIdenticalKeys is the runnable completeness check for the
// interface translations: every English key must exist in French and Spanish,
// and neither may carry a key English lacks, so no user-visible string can be
// left untranslated.
func TestTranslationsHaveIdenticalKeys(t *testing.T) {
	data, err := os.ReadFile("static/i18n.json")
	if err != nil {
		t.Fatalf("read static/i18n.json: %v", err)
	}
	var tables map[string]map[string]string
	if err := json.Unmarshal(data, &tables); err != nil {
		t.Fatalf("parse static/i18n.json: %v", err)
	}

	en := tables["en"]
	if len(en) == 0 {
		t.Fatal("English translation table is empty")
	}
	for _, lang := range []string{"fr", "es"} {
		table := tables[lang]
		if len(table) == 0 {
			t.Errorf("language %q has no translations", lang)
			continue
		}
		for key := range en {
			if _, ok := table[key]; !ok {
				t.Errorf("language %q is missing key %q", lang, key)
			}
		}
		for key := range table {
			if _, ok := en[key]; !ok {
				t.Errorf("language %q has key %q that English lacks", lang, key)
			}
		}
	}
}

// TestFrontendLanguagesMatchNameData keeps the frontend's hardcoded language
// list in step with the embedded name data (validLang): every bird-name
// language the server accepts must be selectable, and every selectable one
// must have a label in each interface language.
func TestFrontendLanguagesMatchNameData(t *testing.T) {
	js, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	m := regexp.MustCompile(`const VALID_LANGS = \[([^\]]*)\]`).FindSubmatch(js)
	if m == nil {
		t.Fatal("VALID_LANGS not found in static/app.js")
	}
	frontend := map[string]bool{}
	for _, q := range regexp.MustCompile(`"([a-z]+)"`).FindAllSubmatch(m[1], -1) {
		frontend[string(q[1])] = true
	}
	for lang := range validLang {
		if !frontend[lang] {
			t.Errorf("language %q has name data but is missing from VALID_LANGS", lang)
		}
	}
	for lang := range frontend {
		if !validLang[lang] {
			t.Errorf("VALID_LANGS lists %q but there is no name data for it", lang)
		}
	}

	data, err := os.ReadFile("static/i18n.json")
	if err != nil {
		t.Fatalf("read static/i18n.json: %v", err)
	}
	var tables map[string]map[string]string
	if err := json.Unmarshal(data, &tables); err != nil {
		t.Fatalf("parse static/i18n.json: %v", err)
	}
	for ui, table := range tables {
		for lang := range frontend {
			if table["lang."+lang] == "" {
				t.Errorf("interface language %q has no label lang.%s", ui, lang)
			}
		}
	}
}
