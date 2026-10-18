package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
)

// KeepRuns is how many runs a history keeps. Only the last
// abbreviatory.FlakyWindow keep their failures and passing tests; older ones
// keep their counts, for the history command.
const KeepRuns = 50

// HistoryMaxAge is how long a history nobody added to is kept.
const HistoryMaxAge = 90 * 24 * time.Hour

// History is the run history of one key: one command in one project.
type History struct {
	Key  string             `json:"key"`
	Runs []abbreviatory.Run `json:"runs"`
}

// Last returns the latest run, or nil.
func (h *History) Last() *abbreviatory.Run {
	if len(h.Runs) == 0 {
		return nil
	}
	return &h.Runs[len(h.Runs)-1]
}

func (s *Store) historyDir() string { return filepath.Join(s.Dir, "history") }

func (s *Store) historyPath(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(s.historyDir(), hex.EncodeToString(h[:8])+".json")
}

// History returns the history of key; an empty one if there is none yet.
func (s *Store) History(key string) (*History, error) {
	data, err := os.ReadFile(s.historyPath(key))
	if errors.Is(err, fs.ErrNotExist) {
		return &History{Key: key}, nil
	}
	if err != nil {
		return nil, err
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, errors.New("store: damaged history for " + key + ": " + err.Error())
	}
	if h.Key != key {
		// A hash collision, however unlikely: don't mix two histories.
		return &History{Key: key}, nil
	}
	return &h, nil
}

// Update locks key's history, reads it and hands it to fn, which compares a
// new run with it, and appends the run fn returns (nothing if it returns
// nil). Holding the lock throughout means two runs of the same key at once
// take turns: the second compares with the first. A damaged history is
// handed over empty, and replaced.
func (s *Store) Update(key string, fn func(h *History) (*abbreviatory.Run, error)) error {
	return s.UpdateCancel(key, nil, fn)
}

// ErrCancelled is returned when the wait for a history's lock was cancelled.
var ErrCancelled = errors.New("store: stopped waiting for the history lock")

// UpdateCancel is Update with a channel that, when closed, stops the wait for
// the lock.
func (s *Store) UpdateCancel(key string, cancel <-chan struct{}, fn func(h *History) (*abbreviatory.Run, error)) error {
	if err := os.MkdirAll(s.historyDir(), 0o700); err != nil {
		return err
	}
	unlock, err := s.lock(s.historyPath(key)+".lock", cancel)
	if err != nil {
		return err
	}
	defer unlock()
	h, err := s.History(key)
	if err != nil {
		h = &History{Key: key}
	}
	run, err := fn(h)
	if err != nil || run == nil {
		return err
	}
	return s.save(h, run)
}

// Record appends a run to key's history. A run numbered at or before the
// latest one (another run was recorded since the history was read) is
// numbered after it; use Update to compare and record under one lock.
func (s *Store) Record(key string, run *abbreviatory.Run) error {
	return s.Update(key, func(h *History) (*abbreviatory.Run, error) {
		if last := h.Last(); last != nil && run.N <= last.N {
			run.N = last.N + 1
		}
		return run, nil
	})
}

func (s *Store) save(h *History, run *abbreviatory.Run) error {
	h.Runs = append(h.Runs, *run)
	if len(h.Runs) > KeepRuns {
		h.Runs = h.Runs[len(h.Runs)-KeepRuns:]
	}
	for i := 0; i < len(h.Runs)-abbreviatory.FlakyWindow; i++ {
		h.Runs[i].Trim()
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return writeAtomic(s.historyPath(h.Key), data)
}

// Histories lists every key with a history, most recently run first.
func (s *Store) Histories() ([]*History, error) {
	ents, err := os.ReadDir(s.historyDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type entry struct {
		h   *History
		mod time.Time
	}
	var list []entry
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(s.historyDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var h History
		if json.Unmarshal(data, &h) != nil || h.Key == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, entry{&h, info.ModTime()})
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].mod.Equal(list[j].mod) {
			return list[i].mod.After(list[j].mod)
		}
		return list[i].h.Key < list[j].h.Key
	})
	out := make([]*History, len(list))
	for i, e := range list {
		out[i] = e.h
	}
	return out, nil
}

// pruneHistories removes histories nobody added to for HistoryMaxAge, and
// lock files left by a run that was killed.
func (s *Store) pruneHistories(now time.Time) {
	ents, err := os.ReadDir(s.historyDir())
	if err != nil {
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		age := now.Sub(info.ModTime())
		name := e.Name()
		switch {
		case (strings.HasSuffix(name, ".lock") || strings.Contains(name, ".lock.stale-")) && age > time.Hour:
			os.Remove(filepath.Join(s.historyDir(), name))
		case strings.HasPrefix(name, ".tmp-") && age > 24*time.Hour:
			os.Remove(filepath.Join(s.historyDir(), name))
		case strings.HasSuffix(name, ".json") && age > HistoryMaxAge:
			os.Remove(filepath.Join(s.historyDir(), name))
		}
	}
}

// Lock timings: a holder touches its lock every lockTouch, so a lock
// untouched for lockStale was left by a run that died; a run waits at most
// lockWait for a lock.
var (
	lockTouch = 20 * time.Second
	lockStale = 2 * time.Minute
	lockWait  = 5 * time.Minute
)

// lock takes a lock file, waiting for another run to let go of it, and
// keeps it fresh until it is released.
func (s *Store) lock(path string, cancel <-chan struct{}) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			// A token of our own, so releasing never removes another run's
			// lock.
			token := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			f.WriteString(token)
			f.Close()
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				t := time.NewTicker(lockTouch)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case now := <-t.C:
						os.Chtimes(path, now, now)
					}
				}
			}()
			return func() {
				close(stop)
				<-done
				if b, err := os.ReadFile(path); err == nil && string(b) == token {
					os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if info, serr := os.Stat(path); serr == nil && time.Since(info.ModTime()) > lockStale {
			// Left by a run that died. Moving it aside first means two
			// waiters can't both remove it and then each other's new lock.
			aside := path + ".stale-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if os.Rename(path, aside) == nil {
				if info, err := os.Stat(aside); err == nil && time.Since(info.ModTime()) <= lockStale {
					// Another waiter got there first and this is its new
					// lock: put it back.
					os.Link(aside, path)
				}
				os.Remove(aside)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("store: history is locked by another run (" + path + ")")
		}
		select {
		case <-cancel:
			return nil, ErrCancelled
		case <-time.After(50 * time.Millisecond):
		}
	}
}
