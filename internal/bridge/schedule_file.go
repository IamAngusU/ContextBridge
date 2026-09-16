package bridge

import (
	"errors"
	"os"
	"path/filepath"
)

// writeScheduleFileAtomic keeps the last complete schedule checkpoint visible
// across crashes. A unique same-directory temporary also prevents a stale temp
// file from an interrupted process from colliding with the next write.
func writeScheduleFileAtomic(path string, data []byte) (returnErr error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = closeErr
			}
		}
		if removeErr := os.Remove(temporaryPath); returnErr == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = removeErr
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	if err := replaceScheduleFile(temporaryPath, path); err != nil {
		return err
	}
	return syncScheduleDirectory(directory)
}
