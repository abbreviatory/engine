package parse

import (
	"fmt"
	"testing"
)

// TestFieldTable runs 1,200 passing cases.
func TestFieldTable(t *testing.T) {
	for i := 0; i < 1200; i++ {
		in := fmt.Sprintf("k%d=%d", i, i*7)
		t.Run(fmt.Sprintf("case_%04d", i), func(t *testing.T) {
			k, v, err := Field(in)
			if err != nil || k != fmt.Sprintf("k%d", i) || v != i*7 {
				t.Fatalf("Field(%q) = %q, %d, %v", in, k, v, err)
			}
		})
	}
}

func TestFieldNegative(t *testing.T) {
	_, v, err := Field("x=-3")
	if err != nil || v != -3 {
		t.Fatal(err)
	}
}

// The failures below are deliberate.

func TestFieldSpaces(t *testing.T) {
	_, v, err := Field("width = 12")
	if err != nil {
		t.Errorf("Field(%q): unexpected error: %v", "width = 12", err)
	}
	if v != 12 {
		t.Errorf("Field(%q) = %d, want 12", "width = 12", v)
	}
}

func TestFieldHex(t *testing.T) {
	_, v, err := Field("mask=0xff")
	if err != nil {
		t.Fatalf("Field(%q): %v", "mask=0xff", err)
	}
	t.Log(v)
}

func TestFieldUnits(t *testing.T) {
	for _, in := range []string{"timeout=30s", "size=4KB", "rate=12/s"} {
		t.Run(in, func(t *testing.T) {
			if _, _, err := Field(in); err != nil {
				t.Errorf("Field(%q): %v", in, err)
			}
		})
	}
}

func TestFieldEmpty(t *testing.T) {
	k, _, err := Field("=")
	if err == nil {
		t.Errorf("Field(%q) gave key %q and no error", "=", k)
	}
}

func TestSkipped(t *testing.T) {
	t.Skip("needs a network")
}
