//go:build !windows

package updater

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceExecutable(current, staged, expectedVersion string) (bool, error) {
	backup := current + ".previous"
	next := current + ".next"
	_ = os.Remove(next)
	if err := copyFile(staged, next, 0755); err != nil {
		return false, err
	}
	if err := validateExecutable(next, expectedVersion); err != nil {
		_ = os.Remove(next)
		return false, err
	}
	_ = os.Remove(backup)
	if err := os.Rename(current, backup); err != nil {
		_ = os.Remove(next)
		return false, fmt.Errorf("preserve current executable: %w", err)
	}
	if err := os.Rename(next, current); err != nil {
		_ = os.Rename(backup, current)
		return false, fmt.Errorf("activate updated executable: %w", err)
	}
	if err := validateExecutable(current, expectedVersion); err != nil {
		failed := current + ".failed"
		_ = os.Remove(failed)
		_ = os.Rename(current, failed)
		if rollbackErr := os.Rename(backup, current); rollbackErr != nil {
			return false, fmt.Errorf("updated executable failed validation (%v) and rollback failed: %w", err, rollbackErr)
		}
		return false, fmt.Errorf("updated executable failed validation and was rolled back: %w", err)
	}
	return true, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := output.ReadFrom(input); err != nil {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
