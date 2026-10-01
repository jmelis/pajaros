package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteFileAtomicConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x-0.jpg")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := bytes.Repeat([]byte{byte('a' + i)}, 1<<16)
			if err := writeFileAtomic(path, data); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1<<16 || len(bytes.Trim(got, string(got[:1]))) != 0 {
		t.Error("final file is not one writer's complete content")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestTryLockSpeciesExcludesOtherHolders(t *testing.T) {
	c, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unlock, ok, err := c.tryLockSpecies("Parus major")
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%v err=%v", ok, err)
	}
	if _, ok, err := c.tryLockSpecies("Parus major"); err != nil || ok {
		t.Errorf("second lock while held: ok=%v err=%v, want not ok", ok, err)
	}
	if u, ok, err := c.tryLockSpecies("Cyanistes caeruleus"); err != nil || !ok {
		t.Errorf("other species should lock independently: ok=%v err=%v", ok, err)
	} else {
		u()
	}
	unlock()
	u, ok, err := c.tryLockSpecies("Parus major")
	if err != nil || !ok {
		t.Fatalf("lock after release: ok=%v err=%v", ok, err)
	}
	u()
}

func TestHeldLockMakesFetchesSkipWithoutSideEffects(t *testing.T) {
	c, err := NewImageCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const sci = "Parus major"
	unlock, ok, err := c.tryLockSpecies(sci)
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	defer unlock()

	// Another process holds the lock: neither call may hit Wikimedia (this
	// test would hang or fail on the network) or leave markers behind.
	if err := c.ensureFirstImage(sci); err != nil {
		t.Errorf("ensureFirstImage: %v", err)
	}
	if err := c.topUpImages(sci); err != nil {
		t.Errorf("topUpImages: %v", err)
	}
	for _, p := range []string{c.missingMarkerPath(sci), c.toppedMarkerPath(sci), c.imagePath(sci, 0)} {
		if fileExists(p) {
			t.Errorf("%s should not exist", p)
		}
	}
}

func TestCachedSlugsIgnoresLockAndTempFiles(t *testing.T) {
	dir := t.TempDir()
	c, err := NewImageCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	const sci = "Parus major"
	seedSpecies(t, c, sci, 1, false)
	for _, p := range []string{c.lockPath(sci), c.metaPath(sci) + ".123.tmp"} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	slugs, err := cachedSlugs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(slugs) != 1 || slugs[0] != slugify(sci) {
		t.Errorf("cachedSlugs = %v, want just %q", slugs, slugify(sci))
	}
}
