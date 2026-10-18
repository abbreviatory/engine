package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
