package abbreviatory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The inputs of the independent review before v0.4.0, in testdata/review04.
func review04(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "review04", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReview04Promise(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "review04", "*"))
	if len(files) == 0 {
		t.Fatal("no review inputs")
	}
	for _, f := range files {
		name := filepath.Base(f)
		data := review04(t, name)
		res, err := Reduce(bytes.NewReader(data), Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checkCoverage(t, name, data, res)
		for _, b := range []int{512, 1000, 4096} {
			for _, p := range preserveSets {
				checkPromise(t, name, data, Options{Budget: b, Preserve: p})
			}
		}
	}
}

func review04Text(t *testing.T, name string, budget int) *Result {
	t.Helper()
	res, err := Reduce(bytes.NewReader(review04(t, name)), Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Lines between files, such as diff -r's "Only in", are shown or counted.
func TestReview04DiffOtherLines(t *testing.T) {
	res := review04Text(t, "onlyin.diff", 512)
	if res.Whole {
		t.Skip("shown whole")
	}
	if !strings.Contains(res.Text, "Only in") && !strings.Contains(res.Text, "other kinds") {
		t.Errorf("the Only in lines are neither shown nor counted:\n%s", res.Text)
	}
	res = review04Text(t, "diff-then-log.txt", 1024)
	if !strings.Contains(res.Text, "OTHER (") || !strings.Contains(res.Text, "other kinds") {
		t.Errorf("the log around the diff is neither shown nor counted:\n%s", res.Text)
	}
}

// A combined diff (a merge) counts its lines.
func TestReview04CombinedDiff(t *testing.T) {
	for _, name := range []string{"mixed.diff", "cc.diff"} {
		res, err := Reduce(bytes.NewReader(review04(t, name)), Options{Budget: 512, Format: "diff"})
		if err != nil {
			t.Fatal(err)
		}
		auto := review04Text(t, name, 512)
		if auto.Format != "diff" {
			t.Errorf("%s detected as %s", name, auto.Format)
		}
		if strings.Contains(res.Text, "(+0 -0)") {
			t.Errorf("%s: a combined file counts nothing:\n%s", name, res.Text)
		}
	}
}

// Hunks that share their first 200 changed lines aren't the same change.
func TestReview04HunkIdentity(t *testing.T) {
	res := review04Text(t, "samehead.diff", 4096)
	if !strings.Contains(res.Text, "CHANGES (2)") || strings.Contains(res.Text, "the same change") {
		t.Errorf("two different hunks folded:\n%s", res.Text)
	}
}

func TestReview04HTTPBody(t *testing.T) {
	res := review04Text(t, "http-bodystatus2.txt", 1024)
	if res.Records["error"] != 0 || !strings.Contains(res.Text, "http: 1 response: 200 OK") {
		t.Errorf("a status line in a body started a response:\n%s", res.Text)
	}
	res = review04Text(t, "http-md.txt", 512)
	// The body is read like a log: its ERROR line is an error, its
	// "failed" bullet and its quote aren't curl's.
	if res.Records["error"] != 1 {
		t.Errorf("markdown body lines read as curl's:\n%s", res.Text)
	}
}

func TestReview04NotListing(t *testing.T) {
	if res := review04Text(t, "pipfreeze.txt", 512); res.Format == "listing" {
		t.Errorf("pip freeze detected as a listing")
	}
	// tree's own count: a leaf directory isn't a file.
	res, err := Reduce(bytes.NewReader(review04(t, "tree.txt")), Options{Budget: 512, Format: "listing"})
	if err != nil {
		t.Fatal(err)
	}
	ad := newListing()
	for i, l := range strings.Split(strings.TrimRight(string(review04(t, "tree.txt")), "\n"), "\n") {
		ad.line(int64(i+1), l, func(Record) {})
	}
	if s := ad.finish(func(Record) {})[0]; !strings.HasPrefix(s, "listing: 4 files in 6 directories") {
		t.Errorf("summary %q, want tree's own count; report:\n%s", s, res.Text)
	}
}

// History knows an HTTP error by what it is, not by where it is.
func TestReview04HTTPHistory(t *testing.T) {
	for _, pair := range [][2]string{{"h1.txt", "h2.txt"}, {"j1.txt", "j2.txt"}} {
		first, err := Reduce(bytes.NewReader(review04(t, pair[0])), Options{Track: true})
		if err != nil {
			t.Fatal(err)
		}
		res, err := Reduce(bytes.NewReader(review04(t, pair[1])), Options{Track: true, History: []Run{*first.Run}})
		if err != nil {
			t.Fatal(err)
		}
		d := res.Delta
		if len(d.New) != 0 {
			t.Errorf("%s then %s: new %v\n%s", pair[0], pair[1], d.New, res.Text)
		}
	}
}
