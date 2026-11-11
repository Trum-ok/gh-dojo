package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// DefaultTTL is how long a cached schema is trusted.
const DefaultTTL = time.Hour

// cacheVersion changes whenever the cached JSON shape does; other versions
// are cache misses.
const cacheVersion = 1

// CacheDirEnv overrides the cache directory (tests point it at t.TempDir()).
const CacheDirEnv = "GH_DOJO_CACHE_DIR"

// DefaultCacheDir returns $GH_DOJO_CACHE_DIR, else $XDG_CACHE_HOME/gh-dojo,
// else ~/.cache/gh-dojo (on every OS, macOS included).
func DefaultCacheDir() (string, error) {
	if dir := os.Getenv(CacheDirEnv); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "gh-dojo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "gh-dojo"), nil
}

// Loader reads board schemas through a cache.
type Loader struct {
	Client graph.Querier
	// Host is the GitHub host, part of the cache key. Empty means gh's
	// default host (GH_HOST or gh config).
	Host string
	// Dir is the cache directory. Empty means DefaultCacheDir(); if that
	// fails too, the cache is off.
	Dir string
	// TTL is how long a cached schema is used. Zero means DefaultTTL.
	TTL time.Duration
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// NewLoader returns a Loader with the default cache directory, TTL and clock.
func NewLoader(c graph.Querier) *Loader { return &Loader{Client: c} }

// Load returns the board schema from the cache if it is younger than the
// TTL, else fetches it (one GraphQL request) and caches it.
func (l *Loader) Load(ctx context.Context, ref Ref) (*Schema, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if s := l.readCache(ref); s != nil {
		return s, nil
	}
	return l.Fetch(ctx, ref)
}

// Fetch reads the board schema from GitHub, bypassing the cache, and
// refreshes the cache. User and organization boards are told apart in the
// same request.
func (l *Loader) Fetch(ctx context.Context, ref Ref) (*Schema, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	var resp schemaResponse
	err := l.Client.Query(ctx, schemaQuery, map[string]any{"owner": ref.Owner, "number": ref.Number}, &resp)
	if err != nil {
		if render.FromError(err).Code == render.ExitNotFound {
			return nil, notFound(ref, err)
		}
		return nil, err
	}
	o := resp.RepositoryOwner
	if o == nil {
		return nil, render.NotFound("GitHub user or organization %q not found", ref.Owner)
	}
	if o.ProjectV2 == nil {
		return nil, notFound(ref, nil)
	}
	s := o.ProjectV2.schema(o.Typename, o.Login, ref.Number)
	s.FetchedAt = l.now().UTC()
	l.writeCache(ref, s)
	return s, nil
}

// Invalidate drops the cached schema of the board, so the next Load
// fetches it. A missing cache entry is not an error.
func (l *Loader) Invalidate(ref Ref) error {
	path, ok := l.path(ref)
	if !ok {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Resolve loads the schema and calls fn with it. If fn fails in a way a
// stale cache explains (IsStale: an unknown field, option or iteration, a
// changed field type, or an ID GitHub rejected) and the schema came from
// the cache, the cache is dropped, the board re-read and fn called once
// more. It returns the schema fn last saw.
//
// fn may run a mutation, but then it must re-read whatever state it
// depends on when called again: a rejected mutation may have been partly
// applied.
func (l *Loader) Resolve(ctx context.Context, ref Ref, fn func(*Schema) error) (*Schema, error) {
	s, err := l.Load(ctx, ref)
	if err != nil {
		return nil, err
	}
	err = fn(s)
	if err == nil || !s.Cached || !IsStale(err) {
		return s, err
	}
	_ = l.Invalidate(ref)
	if s, err = l.Fetch(ctx, ref); err != nil {
		return nil, err
	}
	return s, fn(s)
}

func notFound(ref Ref, cause error) error {
	e := render.NotFound("board %s not found", ref).
		WithFix("gh project list --owner " + ref.Owner)
	if cause != nil {
		e = e.WithCause(cause)
	}
	return e
}

func (l *Loader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loader) ttl() time.Duration {
	if l.TTL > 0 {
		return l.TTL
	}
	return DefaultTTL
}

func (l *Loader) path(ref Ref) (string, bool) {
	dir := l.Dir
	if dir == "" {
		var err error
		if dir, err = DefaultCacheDir(); err != nil {
			return "", false
		}
	}
	if ref.Validate() != nil {
		return "", false
	}
	host := l.Host
	if host == "" {
		host, _ = auth.DefaultHost()
	}
	name := fmt.Sprintf("board-%s-%s-%d.json", safeHost(host), strings.ToLower(ref.Owner), ref.Number)
	return filepath.Join(dir, name), true
}

// safeHost makes a host name safe for a file name.
func safeHost(host string) string {
	host = strings.ToLower(host)
	if host == "" {
		return "github.com"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, host)
}

type cacheFile struct {
	Version int     `json:"version"`
	Schema  *Schema `json:"schema"`
}

// readCache returns the cached schema, or nil on a miss: no file, an
// unreadable or corrupt one, another version or board, or an expired entry.
func (l *Loader) readCache(ref Ref) *Schema {
	path, ok := l.path(ref)
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f cacheFile
	if json.Unmarshal(data, &f) != nil || f.Version != cacheVersion || f.Schema == nil {
		return nil
	}
	s := f.Schema
	if !strings.EqualFold(s.Owner, ref.Owner) || s.Number != ref.Number || s.ID == "" {
		return nil
	}
	age := l.now().Sub(s.FetchedAt)
	if age < 0 || age >= l.ttl() {
		return nil
	}
	s.Cached = true
	return s
}

// writeCache saves s atomically (temp file + rename). Failures are ignored:
// the cache is an optimization.
func (l *Loader) writeCache(ref Ref, s *Schema) {
	path, ok := l.path(ref)
	if !ok {
		return
	}
	data, err := json.MarshalIndent(cacheFile{Version: cacheVersion, Schema: s}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	removeStaleTemps(dir, time.Now())
	tmp, err := os.CreateTemp(dir, ".board-*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

// staleTempAge is how old a leftover temp file (from a killed process) must
// be before it is removed; younger ones may belong to a concurrent write.
const staleTempAge = 10 * time.Minute

func removeStaleTemps(dir string, now time.Time) {
	temps, _ := filepath.Glob(filepath.Join(dir, ".board-*.tmp"))
	for _, path := range temps {
		if info, err := os.Stat(path); err == nil && now.Sub(info.ModTime()) > staleTempAge {
			os.Remove(path)
		}
	}
}
