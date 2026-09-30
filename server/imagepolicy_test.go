package main

import (
	"os"
	"path/filepath"
	"testing"
)

func seedSpecies(t *testing.T, c *ImageCache, sci string, n int, topped bool) {
	t.Helper()
	var infos []*ImageInfo
	for i := 0; i < n; i++ {
		if err := os.WriteFile(c.imagePath(sci, i), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		infos = append(infos, &ImageInfo{SourceURL: sci + string(rune('a'+i))})
	}
	if err := c.writeMetadata(sci, infos); err != nil {
		t.Fatal(err)
	}
	if topped {
		os.WriteFile(c.toppedMarkerPath(sci), nil, 0o644)
	}
}

func TestMigrateImagePolicyKeepsTitleImageOnly(t *testing.T) {
	dir := t.TempDir()
	c, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedSpecies(t, c, "Parus major", 4, true)
	seedSpecies(t, c, "Erithacus rubecula", 1, true)
	os.WriteFile(c.missingMarkerPath("Certhia brachydactyla"), nil, 0o644)

	if err := migrateImagePolicy(c, dir); err != nil {
		t.Fatal(err)
	}

	for _, sci := range []string{"Parus major", "Erithacus rubecula"} {
		if !fileExists(c.imagePath(sci, 0)) {
			t.Errorf("%s: title image was removed", sci)
		}
		for i := 1; i < maxImagesPerSpecies; i++ {
			if fileExists(c.imagePath(sci, i)) {
				t.Errorf("%s: slot %d survived", sci, i)
			}
		}
		if fileExists(c.toppedMarkerPath(sci)) {
			t.Errorf("%s: topped marker survived", sci)
		}
		if got := c.readMetadata(sci); len(got) != 1 {
			t.Errorf("%s: metadata has %d images, want 1", sci, len(got))
		}
	}
	if fileExists(c.missingMarkerPath("Certhia brachydactyla")) {
		t.Error("missing marker survived")
	}
}

func TestMigrateImagePolicyRunsOnce(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewImageCache(dir)
	seedSpecies(t, c, "Parus major", 4, true)
	if err := migrateImagePolicy(c, dir); err != nil {
		t.Fatal(err)
	}

	// Simulate the background refill that follows the migration.
	seedSpecies(t, c, "Parus major", 3, true)
	if err := migrateImagePolicy(c, dir); err != nil {
		t.Fatal(err)
	}

	if !fileExists(c.imagePath("Parus major", 2)) || !fileExists(c.toppedMarkerPath("Parus major")) {
		t.Fatal("second startup re-trimmed refilled photos")
	}
	if _, err := os.Stat(filepath.Join(dir, imagePolicyFile)); err != nil {
		t.Fatalf("policy marker missing: %v", err)
	}
}

func TestMigrateImagePolicyFreshCacheRecordsPolicy(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewImageCache(dir)
	if err := migrateImagePolicy(c, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, imagePolicyFile)); err != nil {
		t.Fatalf("policy marker missing: %v", err)
	}
	if fileExists(filepath.Join(dir, migrationLogName)) {
		t.Error("fresh cache should not create a migration log")
	}
}

func TestAllowedLicense(t *testing.T) {
	for _, l := range []string{"CC BY 2.0", "CC BY-SA 4.0", "CC0", "Public domain", "CC BY-SA 3.0"} {
		if !licenseAllowed(l) {
			t.Errorf("%q should be allowed", l)
		}
	}
	for _, l := range []string{"CC BY-NC 2.0", "CC BY-ND 2.0", "CC BY-NC-SA 4.0", "All rights reserved", ""} {
		if licenseAllowed(l) {
			t.Errorf("%q should be rejected", l)
		}
	}
}
