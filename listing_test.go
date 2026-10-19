package abbreviatory

import (
	"strings"
	"testing"
)

func reduceAs(t *testing.T, in, want string, budget int) *Result {
	t.Helper()
	res, err := Reduce(strings.NewReader(in), Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != want {
		t.Fatalf("detected as %s, want %s", res.Format, want)
	}
	checkCoverage(t, want, []byte(in), res)
	return res
}

func wantIn(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in\n%s", w, text)
		}
	}
}

func TestListingTree(t *testing.T) {
	// tree uses no-break spaces in its indent.
	in := "d\n├── a\n│\u00a0\u00a0 ├── b\n│\u00a0\u00a0 │\u00a0\u00a0 └── y.go\n│\u00a0\u00a0 └── x.go\n└── c\n    ├── link -> ../a/x.go\n    └── z.txt\n\n3 directories, 4 files\n"
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(strings.Replace(in, "d\n", "", 1))
	}
	res := reduceAs(t, "d\n"+b.String(), "listing", 1024)
	wantIn(t, res.Text, "listing: 160 files in ", "a/b/*.go", "c/* (no extension)")
}

func TestListingDu(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 31; i++ {
		b.WriteString("4\tp/d" + itoa(i) + "/f.go\n8\tp/d" + itoa(i) + "\n")
	}
	b.WriteString("300\tp\n")
	res := reduceAs(t, b.String(), "listing", 600)
	// Directories aren't counted as files, and the total isn't the sum of
	// the rows (directories include what's in them).
	wantIn(t, res.Text, "listing: 31 files in 32 directories, 307.2 kB")
}

func TestListingLsAndErrors(t *testing.T) {
	in := "total 12\n" +
		"drwxr-xr-x 2 root root 4096 Oct 19 07:04 .\n" +
		"drwxr-xr-x 4 root root 4096 Oct 19 07:04 ..\n" +
		"lrwxrwxrwx 1 root root    9 Oct 19 07:04 link -> ../a/x.go\n" +
		"-rw-r--r-- 1 root root 2048 Oct 19 07:04 z.txt\n" +
		"-rw-r--r-- 1 root root 1048576 2026-10-19 07:04 big.bin\n" +
		"ls: cannot open directory 'secret': Permission denied\n"
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(in)
	}
	res := reduceAs(t, b.String(), "listing", 1024)
	wantIn(t, res.Text, "ERRORS (1 kind, 5 lines):", "Permission denied", "largest: big.bin 1.0 MB")
	if strings.Contains(res.Text, "link -> ") {
		t.Errorf("a symlink's target is part of its name:\n%s", res.Text)
	}
}

func TestListingFindPaths(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("./src/pkg/file" + itoa(i) + ".go\n./src/pkg/file" + itoa(i) + "_test.go\n./docs/page" + itoa(i) + ".md\n")
	}
	b.WriteString("find: './private': Permission denied\n")
	res := reduceAs(t, b.String(), "listing", 1024)
	wantIn(t, res.Text, "listing: 150 files in 2 directories; most: .go 100, .md 50", "- x100 src/pkg/*.go", "- x50 docs/*.md", "find: './private': Permission denied")
}

// Logs and prose aren't listings, even with paths in them.
func TestListingNotLogs(t *testing.T) {
	for _, in := range []string{
		"2026-10-19 07:00:00 INFO loading /etc/app.conf\n2026-10-19 07:00:01 INFO loaded 4 modules\n2026-10-19 07:00:02 ERROR failed: /var/run/app.sock\n2026-10-19 07:00:03 INFO retry\n2026-10-19 07:00:04 INFO ok\n2026-10-19 07:00:05 INFO done\n",
		"This is a sentence about src/main.go.\nAnother sentence with words.\nAnd a third one here.\nFour.\nFive lines.\nSix lines of prose.\n",
	} {
		res, err := Reduce(strings.NewReader(in), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Format != "logs" {
			t.Errorf("detected as %s:\n%s", res.Format, in)
		}
	}
}
