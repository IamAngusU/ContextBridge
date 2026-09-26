//go:build !windows

package updater

import (
	"fmt"
	"os"
	"syscall"
)

var restartExec = syscall.Exec

func RestartCurrentProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return restartCurrentProcessAt(executable)
}

func restartCurrentProcessAt(executable string) error {
	// #nosec G702 -- executable comes from os.Executable, not user input, and
	// syscall.Exec receives an argv vector directly without a command shell.
	if err := restartExec(executable, os.Args, os.Environ()); err != nil {
		backup := executable + ".previous"
		failed := executable + ".failed"
		_ = os.Remove(failed)
		if renameErr := os.Rename(executable, failed); renameErr == nil {
			if restoreErr := os.Rename(backup, executable); restoreErr != nil {
				_ = os.Rename(failed, executable)
				return fmt.Errorf("restart failed (%v), rollback failed: %w", err, restoreErr)
			}
			return fmt.Errorf("restart failed; previous executable restored: %w", err)
		}
		return fmt.Errorf("restart failed and rollback could not start: %w", err)
	}
	return nil
}
