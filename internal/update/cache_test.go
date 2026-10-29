package update

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCacheRoundTripIsAtomicAndOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewCacheStoreAt(filepath.Join(dir, "update.json"))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	want := Cache{
		CheckedAt:     now,
		LastAttemptAt: now,
		LatestVersion: "v1.2.3",
		ReleaseID:     "42",
		ReleaseURL:    "https://example.test/releases/42",
		ETag:          `"abc"`,
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := store.Load()
	if !ok {
		t.Fatal("Load missed a cache Save just wrote")
	}
	if got.LatestVersion != want.LatestVersion || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("cache mode = %o, want 600", mode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache directory has %d entries, want only the cache: %v", len(entries), entries)
	}
}

func TestCacheCorruptionIsTreatedAsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	for _, body := range []string{"{", "not json", `{"schema_version":99}`, ``, `{"schema_version":1,"latest_version":"not-a-version"}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		store := NewCacheStoreAt(path)
		if _, ok := store.Load(); ok {
			t.Fatalf("Load accepted corrupt cache %q", body)
		}
	}
}

func TestFreshUsesAttemptTimeSoFailuresAreNotRetriedEveryLaunch(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cache := Cache{LastAttemptAt: now.Add(-2 * time.Hour), CheckedAt: now.Add(-48 * time.Hour)}
	if !Fresh(cache, 24*time.Hour, now) {
		t.Fatal("a failed attempt inside the TTL must suppress a retry")
	}
	if Fresh(cache, time.Hour, now) {
		t.Fatal("an attempt older than the TTL must be stale")
	}
	if Fresh(Cache{}, time.Hour, now) {
		t.Fatal("an empty cache must be stale")
	}
	if Fresh(cache, 0, now) {
		t.Fatal("a non-positive TTL must always be stale")
	}
	future := Cache{LastAttemptAt: now.Add(48 * time.Hour)}
	if Fresh(future, 24*time.Hour, now) {
		t.Fatal("a far-future attempt must not suppress checks indefinitely")
	}
	skew := Cache{LastAttemptAt: now.Add(time.Minute)}
	if !Fresh(skew, 24*time.Hour, now) {
		t.Fatal("a small clock skew should keep the record usable")
	}
}

func TestConcurrentCacheWritesNeverExposeATornDocument(t *testing.T) {
	store := NewCacheStoreAt(filepath.Join(t.TempDir(), "update.json"))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cache := Cache{LatestVersion: "v1.0." + strings.Repeat("1", i%3+1), LastAttemptAt: time.Now().UTC()}
			if err := store.Save(cache); err != nil {
				t.Errorf("Save: %v", err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if cache, ok := store.Load(); ok && cache.LatestVersion == "" {
				t.Errorf("Load observed a partial record: %+v", cache)
			}
		}
	}()
	wg.Wait()
	if _, ok := store.Load(); !ok {
		t.Fatal("final cache is unreadable")
	}
}
