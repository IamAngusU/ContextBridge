//go:build windows

package updater

import (
	"errors"

	"golang.org/x/sys/windows"
)

func lockProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied is not proof that the owner exited.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if windows.GetExitCodeProcess(handle, &exitCode) != nil {
		return true
	}
	return exitCode == 259 // STILL_ACTIVE
}
