package clichat

import (
	"io"
	"os"
	"testing"
)

// captureStdout is duplicated from internal/cli/root_test.go for the tests
// that moved into this package.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- string(data)
	}()
	var once bool
	var result string
	return func() string {
		if !once {
			once = true
			os.Stdout = original
			_ = write.Close()
			result = <-captured
		}
		return result
	}
}
