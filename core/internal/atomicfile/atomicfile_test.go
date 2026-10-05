package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteCreatesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	for _, want := range []string{"first", "second, longer than the first"} {
		if err := Write(path, []byte(want), 0o600); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
}

func TestWritePermissionsAndNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := Write(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
}

func TestWriteFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	// The target is a non-empty directory, so the final rename must fail.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(target, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %d entries", len(entries))
	}
}
