package fileutil

import (
	"fmt"
	"os"
)

// AtomicWriteFile writes data to a file atomically using write-to-temp + rename.
// This prevents data corruption if the process crashes mid-write.
func AtomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	tmpFile := filename + ".tmp"

	if err := os.WriteFile(tmpFile, data, perm); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Sync the temp file to ensure data hits disk
	f, err := os.Open(tmpFile)
	if err == nil {
		f.Sync()
		f.Close()
	}

	// Atomic rename
	if err := os.Rename(tmpFile, filename); err != nil {
		os.Remove(tmpFile) // clean up
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
