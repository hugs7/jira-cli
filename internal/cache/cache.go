// Package cache is a tiny on-disk JSON cache used by jr to avoid
// re-hitting the Jira API for slowly-changing data (boards, board
// configurations, status registries…).
//
// Entries live under $XDG_CACHE_HOME/jr/<host>/<key>.json. Each
// entry stores the fetched timestamp alongside the payload so Get
// can decide whether the cache is fresh enough.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type entry struct {
	Fetched time.Time       `json:"fetched"`
	Data    json.RawMessage `json:"data"`
}

func dir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "jr"), nil
}

// path returns the on-disk path for a host-scoped cache key. Forward
// slashes in the key are collapsed to dashes so callers can use them
// as namespacing without creating subdirectories.
func path(host, key string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	safeHost := safeName(host)
	safeKey := safeName(key)
	return filepath.Join(d, safeHost, safeKey+".json"), nil
}

func safeName(s string) string {
	r := []rune(s)
	for i, c := range r {
		switch c {
		case '/', '\\', ':', ' ':
			r[i] = '-'
		}
	}
	return string(r)
}

// Get reads the cached value into out (a pointer) when it exists and
// is younger than ttl. Returns true on a cache hit. A negative ttl
// means "never expire".
func Get(host, key string, ttl time.Duration, out any) bool {
	p, err := path(host, key)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return false
	}
	if ttl >= 0 && time.Since(e.Fetched) > ttl {
		return false
	}
	return json.Unmarshal(e.Data, out) == nil
}

// Put writes value to the cache, creating directories as needed.
// Errors are ignored by callers in practice — caches are best-effort.
func Put(host, key string, value any) error {
	p, err := path(host, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	out, err := json.Marshal(entry{Fetched: time.Now(), Data: data})
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o600)
}

// Age reports how long ago the entry for (host,key) was written, or
// (0,false) when no entry exists. Useful for displaying "cached N
// minutes ago" hints in the UI.
func Age(host, key string) (time.Duration, bool) {
	p, err := path(host, key)
	if err != nil {
		return 0, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false
	}
	return time.Since(e.Fetched), true
}

// Invalidate deletes the cache entry for (host,key). Used by manual
// refresh. Missing entries are not an error.
func Invalidate(host, key string) error {
	p, err := path(host, key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
