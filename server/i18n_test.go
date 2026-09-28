package main

import (
	"encoding/json"
	"os"
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
