// Package atomicfile writes files so that readers never see a partial write.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces the file at path with data. The data goes to a temporary file
// in the same directory first, is flushed to disk, and is then renamed into
// place, so a crash leaves either the old file or the new one, never a mix.
// The file is created with the given permissions.
func Write(path string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(name)
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("atomicfile: chmod: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("atomicfile: write: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("atomicfile: sync: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	return nil
}
