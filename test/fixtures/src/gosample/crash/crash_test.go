package crash

import "testing"

func TestPickFirst(t *testing.T) {
	if Pick([]int{4, 5}, 0) != 4 {
		t.Fatal("wrong")
	}
}

// TestPickPast panics on purpose.
func TestPickPast(t *testing.T) {
	Pick([]int{4, 5}, 2)
}
