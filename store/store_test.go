package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
)

func reduceInto(t *testing.T, s *Store, input string) *abbreviatory.Result {
	t.Helper()
	p, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res, err := abbreviatory.Reduce(strings.NewReader(input), abbreviatory.Options{Sink: p})
	if err != nil {
		p.Abort()
		t.Fatal(err)
	}
	if err := p.Commit(res.Index); err != nil {
		t.Fatal(err)
	}
	return res
}

func input() string {
	return strings.Repeat("INFO tick\n", 400) + "ERROR disk full\n" + strings.Repeat("WARN slow\n", 30)
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := input()
	res := reduceInto(t, s, in)
	data, ix, err := s.Load(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != in || ix.ID != res.ID || ix.Format != "logs" {
		t.Fatalf("loaded %d bytes, index %+v", len(data), ix)
	}
	for ref, want := range map[string]string{
		res.ID:                      in,
		res.ID + ":error":           "ERROR disk full\n",
		"[ref " + res.ID + ":L401]": "ERROR disk full\n",
		res.ID + ":warn":            strings.Repeat("WARN slow\n", 30),
	} {
		got, err := s.Expand(ref)
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", ref, got, err)
		}
	}
	// Storing the same input again is harmless.
	reduceInto(t, s, in)
	got, err := s.Expand(res.ID)
	if err != nil || string(got) != in {
		t.Errorf("after storing twice: %v", err)
	}
}

func TestStoreMissing(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Expand("0123456789ab:g1"); err == nil || !strings.Contains(err.Error(), "not in the store") {
		t.Errorf("got %v", err)
	}
	if _, _, err := s.Load("0123456789ab"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestStoreAbortLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	p, _ := s.Begin()
	p.Write([]byte("partial"))
	p.Abort()
	ents, _ := os.ReadDir(filepath.Join(dir, "objects"))
	if len(ents) != 0 {
		t.Errorf("left %d files", len(ents))
	}
}

func TestStoreDamaged(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	res := reduceInto(t, s, input())
	os.WriteFile(filepath.Join(dir, "objects", res.ID+".gz"), []byte("not gzip"), 0o600)
	if _, err := s.Expand(res.ID); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("got %v", err)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	old := reduceInto(t, s, input())
	newer := reduceInto(t, s, input()+"ERROR another\n")
	past := time.Now().Add(-10 * 24 * time.Hour)
	os.Chtimes(filepath.Join(dir, "objects", old.ID+".gz"), past, past)

	n, freed, err := s.Prune(DefaultMaxAge, DefaultMaxBytes)
	if err != nil || n != 1 || freed <= 0 {
		t.Fatalf("pruned %d, freed %d, %v", n, freed, err)
	}
	if _, err := s.Expand(old.ID); err == nil {
		t.Error("old original still there")
	}
	if _, err := s.Expand(newer.ID); err != nil {
		t.Errorf("newer original gone: %v", err)
	}
	// A size limit removes the rest.
	n, _, err = s.Prune(DefaultMaxAge, 1)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("ABBREVIATORY_STORE", "/tmp/somewhere")
	if d, err := DefaultDir(); err != nil || d != "/tmp/somewhere" {
		t.Errorf("%q %v", d, err)
	}
}

func TestPruneNowOncePerHour(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.PruneNow()
	info, err := os.Stat(filepath.Join(dir, "last-prune"))
	if err != nil {
		t.Fatal(err)
	}
	s.PruneNow()
	info2, _ := os.Stat(filepath.Join(dir, "last-prune"))
	if !info2.ModTime().Equal(info.ModTime()) {
		t.Error("pruned twice within the hour")
	}
}

func TestHistoryRecord(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.History("k")
	if err != nil || len(h.Runs) != 0 || h.Key != "k" {
		t.Fatalf("new history: %v %+v", err, h)
	}
	for i := 1; i <= 15; i++ {
		run := &abbreviatory.Run{N: i, ID: "x", Bad: map[string]abbreviatory.RunItem{"a": {Head: "boom", Streak: i}}, Full: true}
		if err := s.Record("k", run); err != nil {
			t.Fatal(err)
		}
	}
	// A run numbered behind the history (another run got in first) goes
	// after it.
	late := &abbreviatory.Run{N: 3, Full: true}
	if err := s.Record("k", late); err != nil || late.N != 16 {
		t.Fatalf("late run numbered %d: %v", late.N, err)
	}
	h, _ = s.History("k")
	if len(h.Runs) != 16 {
		t.Fatalf("%d runs", len(h.Runs))
	}
	for i, r := range h.Runs {
		full := i >= len(h.Runs)-abbreviatory.FlakyWindow
		if r.Full != full || (!full && r.Bad != nil) {
			t.Fatalf("run %d: full %v, bad %v", r.N, r.Full, r.Bad)
		}
	}
	s.Record("other", &abbreviatory.Run{N: 1})
	all, err := s.Histories()
	if err != nil || len(all) != 2 {
		t.Fatalf("%d histories: %v", len(all), err)
	}
}

func TestHistoryLock(t *testing.T) {
	s, _ := Open(t.TempDir())
	os.MkdirAll(s.historyDir(), 0o700)
	lock := s.historyPath("k") + ".lock"
	os.WriteFile(lock, nil, 0o600)
	old := time.Now().Add(-2 * time.Minute)
	os.Chtimes(lock, old, old)
	if err := s.Record("k", &abbreviatory.Run{N: 1}); err != nil {
		t.Fatalf("a stale lock should be taken over: %v", err)
	}
	if _, err := os.Stat(lock); err == nil {
		t.Fatal("lock left behind")
	}
}

func TestPruneOldHistory(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.Record("k", &abbreviatory.Run{N: 1})
	old := time.Now().Add(-HistoryMaxAge - time.Hour)
	os.Chtimes(s.historyPath("k"), old, old)
	s.Prune(DefaultMaxAge, DefaultMaxBytes)
	if _, err := os.Stat(s.historyPath("k")); err == nil {
		t.Fatal("an unused history should be pruned")
	}
}

// Two runs of one key at once take turns: the second compares with the
// first, so numbers and streaks stay right.
func TestHistoryUpdateTakesTurns(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.Record("k", &abbreviatory.Run{N: 1, Full: true, Bad: map[string]abbreviatory.RunItem{"a": {Head: "alpha", Streak: 1}}})
	var wg sync.WaitGroup
	seen := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(fails bool) {
			defer wg.Done()
			err := s.Update("k", func(h *History) (*abbreviatory.Run, error) {
				last := h.Last()
				seen <- last.N
				time.Sleep(100 * time.Millisecond)
				run := &abbreviatory.Run{N: last.N + 1, Full: true, Bad: map[string]abbreviatory.RunItem{}}
				if fails {
					streak := 1
					if p, ok := last.Bad["a"]; ok {
						streak = p.Streak + 1
					}
					run.Bad["a"] = abbreviatory.RunItem{Head: "alpha", Streak: streak}
				}
				return run, nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i == 0)
	}
	wg.Wait()
	close(seen)
	var got []int
	for n := range seen {
		got = append(got, n)
	}
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("both runs compared with run %v", got)
	}
	h, _ := s.History("k")
	if len(h.Runs) != 3 || h.Runs[1].N != 2 || h.Runs[2].N != 3 {
		t.Fatalf("runs %+v", h.Runs)
	}
	for _, r := range h.Runs[1:] {
		if it, ok := r.Bad["a"]; ok {
			want := 1
			if p, ok := h.Runs[r.N-2].Bad["a"]; ok {
				want = p.Streak + 1
			}
			if it.Streak != want {
				t.Fatalf("run %d: streak %d, want %d", r.N, it.Streak, want)
			}
		}
	}
}

func TestLockKeptFresh(t *testing.T) {
	defer func(a, b time.Duration) { lockTouch, lockStale = a, b }(lockTouch, lockStale)
	lockTouch, lockStale = 20*time.Millisecond, 150*time.Millisecond
	s, _ := Open(t.TempDir())
	os.MkdirAll(s.historyDir(), 0o700)
	unlock, err := s.lock(s.historyPath("k") + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	// Held and touched: a second taker must wait, not break it.
	got := make(chan bool)
	go func() {
		u, err := s.lock(s.historyPath("k") + ".lock")
		if err == nil {
			u()
		}
		got <- true
	}()
	select {
	case <-got:
		t.Fatal("a held lock was taken over")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	<-got
}
