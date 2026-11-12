package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The checked-in vectors are exactly what the generator writes today.
func TestCheckedInVectorsAreCurrent(t *testing.T) {
	files, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "..", "..", "vectors", "v1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Errorf("%d files checked in, generator writes %d", len(entries), len(files))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s differs from the generator; run: go run ./cmd/vectors -out ../../vectors/v1", name)
		}
	}
}
