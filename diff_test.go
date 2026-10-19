package abbreviatory

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

const showSample = `commit 1418ecd0123456789abcdef0123456789abcdef0
Author: Someone <someone@example.com>
Date:   Sun Oct 18 21:00:00 2026 +0300

    Run history: compare each run with the last

    A longer body line.

diff --git a/history.go b/history.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/history.go
@@ -0,0 +1,3 @@
+package abbreviatory
+
+const FlakyWindow = 10
diff --git a/old.go b/new.go
similarity index 90%
rename from old.go
rename to new.go
index 2222222..3333333 100644
--- a/old.go
+++ b/new.go
@@ -1,4 +1,4 @@
 package abbreviatory
-// old name
+// new name

 func f() {}
diff --git a/logo.png b/logo.png
index 4444444..5555555 100644
Binary files a/logo.png and b/logo.png differ
diff --git a/a/x.go b/a/x.go
index 6666666..7777777 100644
--- a/a/x.go
+++ b/a/x.go
@@ -10,3 +10,3 @@ func x() {
 	start()
-	timeout := 30
+	timeout := 60
 	stop()
diff --git a/b/y.go b/b/y.go
index 6666666..7777777 100644
--- a/b/y.go
+++ b/b/y.go
@@ -20,3 +20,3 @@ func y() {
 	begin()
-	timeout := 30
+	timeout := 60
 	end()
diff --git a/go.sum b/go.sum
index 8888888..9999999 100644
--- a/go.sum
+++ b/go.sum
@@ -1,2 +1,2 @@
-example.com/m v1.0.0 h1:aaaa=
+example.com/m v1.1.0 h1:bbbb=
 example.com/n v1.0.0 h1:cccc=
diff --git a/merge.txt b/merge.txt
index 1212121..3434343 100644
--- a/merge.txt
+++ b/merge.txt
@@ -1,1 +1,5 @@
-base
+<<<<<<< HEAD
+ours
+=======
+theirs
+>>>>>>> branch
diff --git a/notes.md b/notes.md
index 5656565..7878787 100644
--- a/notes.md
+++ b/notes.md
@@ -1 +1 @@
-old line
+new line with trailing space` + " \n"

func TestDiffAdapter(t *testing.T) {
	res, err := Reduce(strings.NewReader(showSample), Options{Budget: 16384})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "diff" {
		t.Fatalf("detected as %s", res.Format)
	}
	text := res.Text
	for _, want := range []string{
		"diff: 8 files changed, +13 -6 (1 added, 1 renamed, 1 binary, 1 generated); 1 commit",
		"COMMITS (1):\n- commit 1418ecd01234: Run history: compare each run with the last",
		"- A history.go (+3 -0) [ref " + res.ID + ":L9-17]",
		"- R old.go -> new.go (+1 -1)",
		"- M logo.png (binary)",
		"- M go.sum (+1 -1) (generated)",
		"ERRORS (1 kind, 7 lines):\n- merge.txt @@ -1,1 +1,5 @@",
		"WARNINGS (1 kind, 3 lines):\n- notes.md @@ -1 +1 @@",
		"- x2 a/x.go @@ -10,3 +10,3 @@ func x() { (the same change in 2 places)",
		"and 1 change in generated or binary files",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "h1:bbbb") {
		t.Error("a generated file's change is spelled out")
	}
	// The folded change expands to both places.
	for i, g := range res.Index.Groups {
		if g.Kind == "info" && len(g.Ranges) == 2 {
			got, _ := Expand([]byte(showSample), res.Index, "g"+itoa(i))
			if !bytes.Contains(got, []byte("func x()")) || !bytes.Contains(got, []byte("func y()")) {
				t.Fatalf("folded change expands to %q", got)
			}
		}
	}
	checkCoverage(t, "show sample", []byte(showSample), res)
}

func TestPlainUnifiedDiff(t *testing.T) {
	in := "--- old/a.txt\t2026-10-18 10:00:00\n+++ new/a.txt\t2026-10-18 11:00:00\n@@ -1,2 +1,2 @@\n-one\n+uno\n two\n--- old/b.txt\t2026-10-18 10:00:00\n+++ new/b.txt\t2026-10-18 11:00:00\n@@ -1 +1 @@\n-x\n+y\n"
	res, err := Reduce(strings.NewReader(in), Options{Budget: 4096, Format: "diff"})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := Reduce(strings.NewReader(in), Options{})
	if r2.Format != "diff" {
		t.Fatalf("plain diff -u detected as %s", r2.Format)
	}
	if res.Whole {
		// Small enough to show whole; check the parse through the index.
		if len(res.Index.Groups) != 4 {
			t.Fatalf("%d groups, want 2 file headers and 2 hunks", len(res.Index.Groups))
		}
	}
	checkCoverage(t, "plain diff", []byte(in), res)
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString(in)
	}
	big, err := Reduce(strings.NewReader(b.String()), Options{Budget: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(big.Text, "diff: 120 files changed, +120 -120") || !strings.Contains(big.Text, "- M new/a.txt (+1 -1)") {
		t.Fatalf("got:\n%s", big.Text)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
