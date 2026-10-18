// Package parse is a small sample used to produce real go test output for
// Abbreviatory's fixtures. Some of its tests fail on purpose.
package parse

import (
	"fmt"
	"strconv"
	"strings"
)

// Field splits "key=value" and parses the value as an integer.
func Field(s string) (string, int, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", 0, fmt.Errorf("no '=' in %q", s)
	}
	n, err := strconv.Atoi(v)
	return k, n, err
}
