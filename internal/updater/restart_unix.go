//go:build !windows

package updater

import (
	"os"
	"syscall"
)

func RestartCurrentProcess() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
