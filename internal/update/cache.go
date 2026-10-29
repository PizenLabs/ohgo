package update

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/state"
)

// CacheSchemaVersion guards the on-disk shape. A record written by a newer
// build is treated as unreadable rather than interpreted with missing fields.
const CacheSchemaVersion = 1

// maxCachedError bounds the diagnostic retained in the cache. A release
// endpoint is untrusted input; an unbounded string would let a hostile or
// broken server grow machine state.
const maxCachedError = 500

// Cache is the machine-scoped observation of the newest release.
//
// It is a cache, not authority: every field is a hint. The explicit update
// path resolves the release again and verifies its artifact, so a tampered
// cache can at most produce a wrong notification or a redundant request.
type Cache struct {
	// SchemaVersion is CacheSchemaVersion for records this build writes.
	SchemaVersion int `json:"schema_version"`
	// CheckedAt is when a release response was last accepted. Zero means no
	// successful observation exists.
	CheckedAt time.Time `json:"checked_at,omitempty"`
	// LastAttemptAt is when a check was last tried, successful or not. It
	// drives the TTL so a failing endpoint is not retried on every launch.
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	// LatestVersion is the newest release tag observed, e.g. "v1.2.3".
	LatestVersion string `json:"latest_version,omitempty"`
	// ReleaseID is the publisher's stable release identifier.
	ReleaseID string `json:"release_id,omitempty"`
	// ReleaseURL is the human-facing release page. It is provenance only and
	// is never fetched automatically.
	ReleaseURL string `json:"release_url,omitempty"`
	// ETag is the conditional-request validator for the release metadata.
	ETag string `json:"etag,omitempty"`
	// LastError is the most recent check failure, bounded and free of
	// credentials. It is diagnostic, never an instruction.
	LastError string `json:"last_error,omitempty"`
}

// CacheStore owns the update cache file.
//
// The zero value is not usable; construct one with NewCacheStore or
// NewCacheStoreAt. Reads are tolerant by design: a missing, unreadable,
// malformed, or foreign cache is reported as absent so a corrupt machine file
// can never block startup.
type CacheStore struct {
	path string
	now  func() time.Time
}

// NewCacheStore returns the store at the machine-level state path
// (~/.ohgo/update.json).
func NewCacheStore() (*CacheStore, error) {
	path, err := state.Path("update.json")
	if err != nil {
		return nil, fmt.Errorf("update cache path: %w", err)
	}
	return &CacheStore{path: path, now: time.Now}, nil
}

// NewCacheStoreAt returns a store rooted at an explicit path. It exists so a
// test can keep the cache in a temporary directory without touching the real
// machine state.
func NewCacheStoreAt(path string) *CacheStore {
	return &CacheStore{path: path, now: time.Now}
}

// Path returns the cache file path.
func (s *CacheStore) Path() string { return s.path }

// Load reads the cache. ok is false when the file is absent or unusable; err
// is reported only for a path that exists but cannot be read for a reason
// other than absence. Callers treat every non-ok result as a miss.
func (s *CacheStore) Load() (Cache, bool) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Cache{}, false
	}
	var cache Cache
	if err := json.Unmarshal(data, &cache); err != nil {
		return Cache{}, false
	}
	if cache.SchemaVersion != CacheSchemaVersion {
		return Cache{}, false
	}
	cache.CheckedAt = cache.CheckedAt.UTC()
	cache.LastAttemptAt = cache.LastAttemptAt.UTC()
	if cache.CheckedAt.IsZero() && cache.LastAttemptAt.IsZero() && cache.LatestVersion == "" {
		return Cache{}, false
	}
	if cache.LatestVersion != "" {
		// A version that cannot be parsed is corruption, not an observation.
		// Treating it as a miss forces a refresh instead of suppressing checks
		// behind a fresh but unusable record.
		if _, ok := ParseVersion(cache.LatestVersion); !ok {
			return Cache{}, false
		}
	}
	// The validator becomes an HTTP header. A local record must not be able to
	// inject a second header or another request line; a bad value simply means
	// the next request is unconditional.
	if len(cache.ETag) > 512 || strings.ContainsAny(cache.ETag, "\r\n\x00") {
		cache.ETag = ""
	}
	return cache, true
}

// Save writes the cache atomically with owner-only permissions.
//
// The temp file lives next to the destination so the rename is atomic on the
// same filesystem. A crash therefore leaves either the old record or the new
// one, never a truncated document that every later invocation would have to
// recover from.
func (s *CacheStore) Save(cache Cache) error {
	cache.SchemaVersion = CacheSchemaVersion
	cache.CheckedAt = cache.CheckedAt.UTC()
	cache.LastAttemptAt = cache.LastAttemptAt.UTC()
	if len(cache.LastError) > maxCachedError {
		cache.LastError = cache.LastError[:maxCachedError]
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("update: encode cache: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(s.path, data, 0o600)
}

// Fresh reports whether cache is younger than ttl.
//
// Freshness uses LastAttemptAt, not CheckedAt: a persistent endpoint failure
// must still suppress retries for one TTL instead of turning every launch into
// a network wait. A zero or negative ttl always means stale.
func Fresh(cache Cache, ttl time.Duration, now time.Time) bool {
	if ttl <= 0 {
		return false
	}
	at := cache.LastAttemptAt
	if at.IsZero() {
		at = cache.CheckedAt
	}
	if at.IsZero() {
		return false
	}
	if at.After(now) {
		// A modest clock skew leaves the record usable. A far-future value is
		// corrupt or hostile and must not suppress checks indefinitely.
		return at.Sub(now) <= ttl
	}
	return now.Sub(at) < ttl
}
