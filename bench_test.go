package abbreviatory

import (
	"bytes"
	"os"
	"testing"
)

func BenchmarkReduceLog(b *testing.B) {
	data, err := os.ReadFile("test/fixtures/app.log")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := Reduce(bytes.NewReader(data), Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReduceGoTestJSON(b *testing.B) {
	data, err := os.ReadFile("test/fixtures/gotest-json.txt")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := Reduce(bytes.NewReader(data), Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
