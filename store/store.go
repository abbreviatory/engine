// Package store keeps the originals of reduced inputs, so that references in
// a reduction can be expanded later. Each original is stored gzipped next to
// a small index of which lines each group came from. Everything stays on the
// local disk.
package store

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
)

// Defaults for pruning: keep a week, at most 500 MB.
const (
	DefaultMaxAge   = 7 * 24 * time.Hour
	DefaultMaxBytes = 500 << 20
)

// Store is a directory of stored originals.
type Store struct {
	Dir string
}

// DefaultDir is $ABBREVIATORY_STORE, or "abbreviatory" in the user's cache
// directory.
func DefaultDir() (string, error) {
	if d := os.Getenv("ABBREVIATORY_STORE"); d != "" {
		return d, nil
	}
	c, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("no cache directory to store originals in; set ABBREVIATORY_STORE or pass -store")
	}
	return filepath.Join(c, "abbreviatory"), nil
}

// Open opens (creating if needed) a store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) path(id, ext string) string {
	return filepath.Join(s.Dir, "objects", id+ext)
}

// Pending is an original being written while it is reduced.
type Pending struct {
	s       *Store
	f       *os.File
	gz      *gzip.Writer
	done    bool
	touched time.Time
}

// Begin starts storing an original. Write the input to it, then Commit with
// the reduction's index, or Abort.
func (s *Store) Begin() (*Pending, error) {
	f, err := os.CreateTemp(filepath.Join(s.Dir, "objects"), ".tmp-*")
	if err != nil {
		return nil, err
	}
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	return &Pending{s: s, f: f, gz: gz, touched: time.Now()}, nil
}

// Write adds input to the pending original. A slow stream (a long CI job
// piped in) may not flush for a while, so the temporary file's time is
// kept fresh by hand: Prune only removes temporary files left alone for a
// day.
func (p *Pending) Write(b []byte) (int, error) {
	if now := time.Now(); now.Sub(p.touched) > 5*time.Minute {
		p.touched = now
		os.Chtimes(p.f.Name(), now, now)
	}
	return p.gz.Write(b)
}

// Abort throws the pending original away.
func (p *Pending) Abort() {
	if p.done {
		return
	}
	p.done = true
	p.f.Close()
	os.Remove(p.f.Name())
}

// Commit finishes the original and stores it with its index under ix.ID.
func (p *Pending) Commit(ix *abbreviatory.Index) error {
	if p.done {
		return errors.New("store: already finished")
	}
	p.done = true
	err := p.gz.Close()
	if err == nil {
		err = p.f.Sync()
	}
	if cerr := p.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(p.f.Name())
		return err
	}
	if err := os.Rename(p.f.Name(), p.s.path(ix.ID, ".gz")); err != nil {
		os.Remove(p.f.Name())
		return err
	}
	data, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	return writeAtomic(p.s.path(ix.ID, ".idx.json"), data)
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// On Windows a rename over a file someone is reading fails for a
		// moment; try again briefly.
		for i := 0; ; i++ {
			if err = os.Rename(f.Name(), path); err == nil || i == 20 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// ErrNotFound means the reference's original isn't in the store: it was
// never stored (-no-store), was pruned, or is in another store.
var ErrNotFound = errors.New("not in the store (stored with -no-store, pruned, or kept in another store directory)")

// Load returns a stored original and its index.
func (s *Store) Load(id string) ([]byte, *abbreviatory.Index, error) {
	ixData, err := os.ReadFile(s.path(id, ".idx.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var ix abbreviatory.Index
	if err := json.Unmarshal(ixData, &ix); err != nil {
		return nil, nil, errors.New("store: damaged index for " + id + ": " + err.Error())
	}
	f, err := os.Open(s.path(id, ".gz"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, errors.New("store: damaged original for " + id + ": " + err.Error())
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, nil, errors.New("store: damaged original for " + id + ": " + err.Error())
	}
	// Touch it: pruning removes the least recently used first.
	now := time.Now()
	os.Chtimes(s.path(id, ".gz"), now, now)
	return data, &ix, nil
}

// Expand loads a reference's original and returns the part it names.
func (s *Store) Expand(ref string) ([]byte, error) {
	id, sel, err := abbreviatory.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	data, ix, err := s.Load(id)
	if err != nil {
		return nil, errors.New(id + ": " + err.Error())
	}
	return abbreviatory.Expand(data, ix, sel)
}

// Prune removes originals older than maxAge, then the least recently used
// ones until the store holds at most maxBytes. It returns how many it
// removed and the bytes freed.
func (s *Store) Prune(maxAge time.Duration, maxBytes int64) (int, int64, error) {
	dir := filepath.Join(s.Dir, "objects")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	type obj struct {
		id   string
		size int64
		mod  time.Time
	}
	var objs []obj
	var total int64
	now := time.Now()
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			// Left behind by an interrupted run.
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
				os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if strings.HasSuffix(name, ".idx.json") {
			// An index whose original is gone can never be expanded.
			if _, err := os.Stat(s.path(strings.TrimSuffix(name, ".idx.json"), ".gz")); errors.Is(err, fs.ErrNotExist) {
				os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if !strings.HasSuffix(name, ".gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(name, ".gz")
		size := info.Size()
		if ix, err := os.Stat(s.path(id, ".idx.json")); err == nil {
			size += ix.Size()
		}
		objs = append(objs, obj{id, size, info.ModTime()})
		total += size
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].mod.Before(objs[j].mod) })
	s.pruneHistories(now)
	removed := 0
	var freed int64
	for _, o := range objs {
		if now.Sub(o.mod) <= maxAge && total <= maxBytes {
			continue
		}
		os.Remove(s.path(o.id, ".idx.json"))
		if err := os.Remove(s.path(o.id, ".gz")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, freed, err
		}
		removed++
		freed += o.size
		total -= o.size
	}
	return removed, freed, nil
}

// PruneNow runs Prune with the defaults at most once an hour, keeping a
// stamp file to know when it last ran.
func (s *Store) PruneNow() {
	stamp := filepath.Join(s.Dir, "last-prune")
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < time.Hour {
		return
	}
	os.WriteFile(stamp, nil, 0o600)
	now := time.Now()
	os.Chtimes(stamp, now, now)
	s.Prune(DefaultMaxAge, DefaultMaxBytes)
}
