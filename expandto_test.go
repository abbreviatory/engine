package abbreviatory

import (
	"bytes"
	"strconv"
	"testing"
)

// The streaming expand gives the same bytes as Expand, for every selector
// of every fixture.
func TestExpandToMatchesExpand(t *testing.T) {
	checked := 0
	for name, data := range fixtures(t) {
		res, err := Reduce(bytes.NewReader(data), Options{Budget: 4096})
		if err != nil {
			t.Fatal(err)
		}
		sels := []string{"", "fail", "error", "warn", "info", "pass", "skip", "keep", "L1", "L1-3", "L" + strconv.FormatInt(res.InputLines, 10)}
		if res.InputLines > 10 {
			sels = append(sels, "L5-"+strconv.FormatInt(res.InputLines-2, 10))
		}
		for i := range res.Index.Groups {
			sels = append(sels, "g"+strconv.Itoa(i))
		}
		if res.Format == "json" {
			sels = append(sels, "errors", "/items/3", "/items/2/name")
		}
		for _, sel := range sels {
			want, werr := Expand(data, res.Index, sel)
			var got bytes.Buffer
			gerr := ExpandTo(&got, bytes.NewReader(data), res.Index, sel)
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("%s %q: Expand error %v, ExpandTo error %v", name, sel, werr, gerr)
			}
			if werr == nil && !bytes.Equal(want, got.Bytes()) {
				t.Fatalf("%s %q: %d bytes from Expand, %d from ExpandTo", name, sel, len(want), got.Len())
			}
			checked++
		}
	}
	for _, sel := range []string{"g999999", "L0", "nonsense"} {
		var b bytes.Buffer
		ix := &Index{Lines: 3, Groups: []IndexGroup{}}
		if err := ExpandTo(&b, bytes.NewReader([]byte("a\nb\nc\n")), ix, sel); err == nil || b.Len() != 0 {
			t.Fatalf("%q: error %v, wrote %d bytes", sel, err, b.Len())
		}
	}
	// Lines longer than the read buffer, and a last line with no newline.
	long := bytes.Repeat([]byte("x"), 200<<10)
	data := append(append(append([]byte("one\n"), long...), '\n'), []byte("three")...)
	ix := &Index{Lines: 3, Unit: "lines"}
	for _, sel := range []string{"L2", "L2-3", "L3", "L1-3"} {
		want, _ := Expand(data, ix, sel)
		var got bytes.Buffer
		ExpandTo(&got, bytes.NewReader(data), ix, sel)
		if !bytes.Equal(want, got.Bytes()) {
			t.Fatalf("long lines %q: %d vs %d bytes", sel, len(want), got.Len())
		}
	}
	t.Logf("%d selectors checked", checked)
}
